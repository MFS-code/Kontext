package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/runfactory"
	"github.com/MFS-code/Kontext/internal/scheduler"
	"github.com/MFS-code/Kontext/internal/status"
)

const (
	scheduledSlotAnnotation     = "kontext.dev/scheduled-slot"
	scheduledSequenceAnnotation = "kontext.dev/scheduled-sequence"
)

func (r *AgentReconciler) reconcileScheduled(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
) (ctrl.Result, error) {
	now := r.now()
	policy, err := scheduler.Parse(agent.Spec.Schedule)
	if err != nil {
		nextStatus := scheduledBaseStatus(agent, nil)
		nextStatus.ObservedGeneration = agent.Generation
		return ctrl.Result{}, r.setAgentStatus(ctx, agent, nextStatus, metav1.Condition{
			Type:    conditions.Ready,
			Status:  metav1.ConditionFalse,
			Reason:  "InvalidSchedule",
			Message: err.Error(),
		}, metav1.Condition{
			Type:    conditions.Progressing,
			Status:  metav1.ConditionFalse,
			Reason:  "InvalidSchedule",
			Message: "Scheduled reconciliation is blocked by an invalid schedule.",
		})
	}

	runs, err := r.observeScheduledRuns(ctx, agent)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.pruneScheduledHistory(ctx, policy, runs.items); err != nil {
		return ctrl.Result{}, err
	}

	nextStatus := scheduledBaseStatus(agent, &runs)
	nextStatus.ObservedGeneration = agent.Generation

	if agent.Status.ObservedGeneration != agent.Generation || agent.Status.NextScheduleTime == nil {
		next := policy.Schedule.Next(now)
		nextStatus.NextScheduleTime = timePtr(next)
		reason := "ScheduleUpdated"
		message := "Schedule generation observed; waiting for the next future slot."
		if agent.Status.ObservedGeneration == 0 {
			reason = "ScheduleInitialized"
			message = "Schedule initialized; waiting for the first future slot."
		}
		if policy.Suspend {
			reason = "Suspended"
			message = "Scheduled run creation is suspended."
		}
		if err := r.setScheduledWaitingStatus(ctx, agent, nextStatus, reason, message); err != nil {
			return ctrl.Result{}, err
		}
		if policy.Suspend {
			return ctrl.Result{}, nil
		}
		return requeueAt(now, next), nil
	}

	if policy.Suspend {
		nextStatus.NextScheduleTime = timePtr(policy.Schedule.Next(now))
		if err := r.setScheduledWaitingStatus(
			ctx,
			agent,
			nextStatus,
			"Suspended",
			"Scheduled run creation is suspended.",
		); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	firstDue := agent.Status.NextScheduleTime.Time
	due, next := scheduler.LatestDue(policy.Schedule, firstDue, now)
	nextStatus.NextScheduleTime = timePtr(next)
	if due.IsZero() {
		if err := r.setScheduledWaitingStatus(
			ctx,
			agent,
			nextStatus,
			"WaitingForSchedule",
			fmt.Sprintf("Next run is scheduled for %s.", next.UTC().Format(time.RFC3339)),
		); err != nil {
			return ctrl.Result{}, err
		}
		return requeueAt(now, next), nil
	}

	if now.Sub(due) > policy.StartingDeadline {
		if err := r.setScheduledWaitingStatus(
			ctx,
			agent,
			nextStatus,
			"MissedDeadline",
			fmt.Sprintf("Skipped slot %s because its starting deadline expired.", due.UTC().Format(time.RFC3339)),
		); err != nil {
			return ctrl.Result{}, err
		}
		return requeueAt(now, next), nil
	}

	runName := scheduler.RunName(agent.Name, due)
	if existing := runs.bySlot[due.UTC().Format(time.RFC3339)]; existing != nil {
		recoverScheduledRunStatus(&nextStatus, existing, due)
		if err := r.setScheduledCreatedStatus(ctx, agent, nextStatus, existing.Name, due); err != nil {
			return ctrl.Result{}, err
		}
		return requeueAt(now, next), nil
	}

	if policy.ConcurrencyPolicy == kontextv1alpha1.ConcurrencyPolicyForbid && runs.hasActive {
		if err := r.setScheduledWaitingStatus(
			ctx,
			agent,
			nextStatus,
			"OverlapSkipped",
			fmt.Sprintf("Skipped slot %s because an owned run is active.", due.UTC().Format(time.RFC3339)),
		); err != nil {
			return ctrl.Result{}, err
		}
		return requeueAt(now, next), nil
	}

	if nextStatus.RunsCreated == maxRunSuffix {
		return ctrl.Result{}, fmt.Errorf("scheduled run counter exhausted for Agent %s/%s", agent.Namespace, agent.Name)
	}
	sequence := nextStatus.RunsCreated + 1
	run, err := runfactory.NewForAgent(agent, runName, agent.Spec.Goal, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}
	run.Annotations = map[string]string{
		scheduledSlotAnnotation:     due.UTC().Format(time.RFC3339),
		scheduledSequenceAnnotation: strconv.FormatInt(int64(sequence), 10),
	}

	if err := r.Create(ctx, run); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		existing, verifyErr := r.verifyScheduledRun(ctx, agent, runName, due)
		if verifyErr != nil {
			return ctrl.Result{}, verifyErr
		}
		if existing == nil {
			return ctrl.Result{Requeue: true}, nil
		}
		recoverScheduledRunStatus(&nextStatus, existing, due)
		if nextStatus.RunsCreated < sequence {
			nextStatus.RunsCreated = sequence
		}
		if err := r.setScheduledCreatedStatus(ctx, agent, nextStatus, existing.Name, due); err != nil {
			return ctrl.Result{}, err
		}
		return requeueAt(now, next), nil
	}

	nextStatus.RunsCreated = sequence
	nextStatus.LastRunName = run.Name
	nextStatus.LastScheduleTime = timePtr(due)
	if err := r.setScheduledCreatedStatus(ctx, agent, nextStatus, run.Name, due); err != nil {
		return ctrl.Result{}, err
	}
	return requeueAt(now, next), nil
}

type observedScheduledRuns struct {
	items     []kontextv1alpha1.AgentRun
	bySlot    map[string]*kontextv1alpha1.AgentRun
	latest    *kontextv1alpha1.AgentRun
	latestDue time.Time
	maxCount  int32
	hasActive bool
}

func (r *AgentReconciler) observeScheduledRuns(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
) (observedScheduledRuns, error) {
	var children kontextv1alpha1.AgentRunList
	if err := r.List(
		ctx,
		&children,
		client.InNamespace(agent.Namespace),
		client.MatchingLabels{podbuilder.LabelAgentName: agent.Name},
	); err != nil {
		return observedScheduledRuns{}, err
	}

	observed := observedScheduledRuns{
		bySlot: make(map[string]*kontextv1alpha1.AgentRun),
	}
	sort.Slice(children.Items, func(i, j int) bool {
		return children.Items[i].Name < children.Items[j].Name
	})
	for i := range children.Items {
		run := &children.Items[i]
		if !metav1.IsControlledBy(run, agent) {
			continue
		}
		observed.items = append(observed.items, *run.DeepCopy())
		if !status.IsTerminalPhase(run.Status.Phase) {
			observed.hasActive = true
		}

		sequence, err := strconv.ParseInt(run.Annotations[scheduledSequenceAnnotation], 10, 32)
		if err == nil && sequence > int64(observed.maxCount) {
			observed.maxCount = int32(sequence)
		}
		slot, err := time.Parse(time.RFC3339, run.Annotations[scheduledSlotAnnotation])
		if err != nil {
			continue
		}
		slotKey := slot.UTC().Format(time.RFC3339)
		observed.bySlot[slotKey] = run
		if observed.latest == nil || slot.After(observed.latestDue) ||
			(slot.Equal(observed.latestDue) && run.Name > observed.latest.Name) {
			observed.latest = run
			observed.latestDue = slot
		}
	}
	return observed, nil
}

func scheduledBaseStatus(
	agent *kontextv1alpha1.Agent,
	runs *observedScheduledRuns,
) kontextv1alpha1.AgentStatus {
	next := kontextv1alpha1.AgentStatus{
		LastRunName:        agent.Status.LastRunName,
		RunsCreated:        agent.Status.RunsCreated,
		LastScheduleTime:   agent.Status.LastScheduleTime,
		NextScheduleTime:   agent.Status.NextScheduleTime,
		ObservedGeneration: agent.Generation,
	}
	if runs == nil {
		return next
	}
	if runs.maxCount > next.RunsCreated {
		next.RunsCreated = runs.maxCount
	}
	if runs.latest != nil &&
		(next.LastScheduleTime == nil || runs.latestDue.After(next.LastScheduleTime.Time)) {
		next.LastRunName = runs.latest.Name
		next.LastScheduleTime = timePtr(runs.latestDue)
	}
	return next
}

func recoverScheduledRunStatus(
	next *kontextv1alpha1.AgentStatus,
	run *kontextv1alpha1.AgentRun,
	slot time.Time,
) {
	if sequence, err := strconv.ParseInt(run.Annotations[scheduledSequenceAnnotation], 10, 32); err == nil &&
		sequence > int64(next.RunsCreated) {
		next.RunsCreated = int32(sequence)
	}
	if next.LastScheduleTime == nil || !slot.Before(next.LastScheduleTime.Time) {
		next.LastRunName = run.Name
		next.LastScheduleTime = timePtr(slot)
	}
}

func (r *AgentReconciler) verifyScheduledRun(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	runName string,
	slot time.Time,
) (*kontextv1alpha1.AgentRun, error) {
	if r.APIReader == nil {
		return nil, fmt.Errorf(
			"cannot verify existing AgentRun %s/%s: APIReader is not configured",
			agent.Namespace,
			runName,
		)
	}

	var existing kontextv1alpha1.AgentRun
	if err := r.APIReader.Get(
		ctx,
		client.ObjectKey{Namespace: agent.Namespace, Name: runName},
		&existing,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if !metav1.IsControlledBy(&existing, agent) ||
		existing.Annotations[scheduledSlotAnnotation] != slot.UTC().Format(time.RFC3339) {
		return nil, fmt.Errorf(
			"scheduled run name collision: AgentRun %s/%s does not represent slot %s for Agent %s/%s",
			existing.Namespace,
			existing.Name,
			slot.UTC().Format(time.RFC3339),
			agent.Namespace,
			agent.Name,
		)
	}
	return &existing, nil
}

func (r *AgentReconciler) pruneScheduledHistory(
	ctx context.Context,
	policy scheduler.Policy,
	runs []kontextv1alpha1.AgentRun,
) error {
	successful := make([]*kontextv1alpha1.AgentRun, 0)
	failed := make([]*kontextv1alpha1.AgentRun, 0)
	for i := range runs {
		run := &runs[i]
		switch run.Status.Phase {
		case kontextv1alpha1.AgentRunPhaseSucceeded:
			successful = append(successful, run)
		case kontextv1alpha1.AgentRunPhaseFailed, kontextv1alpha1.AgentRunPhaseBudgetExceeded:
			failed = append(failed, run)
		}
	}
	if err := r.pruneScheduledGroup(ctx, successful, policy.SuccessfulRunsHistoryLimit); err != nil {
		return err
	}
	return r.pruneScheduledGroup(ctx, failed, policy.FailedRunsHistoryLimit)
}

func (r *AgentReconciler) pruneScheduledGroup(
	ctx context.Context,
	runs []*kontextv1alpha1.AgentRun,
	limit int32,
) error {
	sort.Slice(runs, func(i, j int) bool {
		iTime := scheduledRunSortTime(runs[i])
		jTime := scheduledRunSortTime(runs[j])
		if iTime.Equal(jTime) {
			return runs[i].Name > runs[j].Name
		}
		return iTime.After(jTime)
	})
	for i := int(limit); i < len(runs); i++ {
		if err := r.Delete(ctx, runs[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune completed scheduled run %s/%s: %w", runs[i].Namespace, runs[i].Name, err)
		}
	}
	return nil
}

func scheduledRunSortTime(run *kontextv1alpha1.AgentRun) time.Time {
	if slot, err := time.Parse(time.RFC3339, run.Annotations[scheduledSlotAnnotation]); err == nil {
		return slot
	}
	if run.Status.CompletionTime != nil {
		return run.Status.CompletionTime.Time
	}
	return run.CreationTimestamp.Time
}

func (r *AgentReconciler) setScheduledWaitingStatus(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	next kontextv1alpha1.AgentStatus,
	reason string,
	message string,
) error {
	return r.setAgentStatus(ctx, agent, next, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}, metav1.Condition{
		Type:    conditions.Progressing,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
}

func (r *AgentReconciler) setScheduledCreatedStatus(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	next kontextv1alpha1.AgentStatus,
	runName string,
	slot time.Time,
) error {
	return r.setAgentStatus(ctx, agent, next, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionTrue,
		Reason:  "RunCreated",
		Message: fmt.Sprintf("Created scheduled run %s for slot %s.", runName, slot.UTC().Format(time.RFC3339)),
	}, metav1.Condition{
		Type:    conditions.Progressing,
		Status:  metav1.ConditionTrue,
		Reason:  "RunCreated",
		Message: fmt.Sprintf("Scheduled run %s is progressing.", runName),
	})
}

func requeueAt(now time.Time, next time.Time) ctrl.Result {
	delay := next.Sub(now)
	if delay <= 0 {
		return ctrl.Result{Requeue: true}
	}
	return ctrl.Result{RequeueAfter: delay}
}

func timePtr(value time.Time) *metav1.Time {
	result := metav1.NewTime(value.UTC())
	return &result
}
