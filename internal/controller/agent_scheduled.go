package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/runfactory"
	"github.com/MFS-code/Kontext/internal/scheduledrun"
	"github.com/MFS-code/Kontext/internal/scheduler"
)

func (r *AgentReconciler) reconcileScheduled(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
) (ctrl.Result, error) {
	now := r.now()
	runs, err := r.observeScheduledRuns(ctx, agent)
	if err != nil {
		return ctrl.Result{}, err
	}

	policy, err := scheduler.Parse(agent.Spec.Schedule)
	if err != nil {
		nextStatus := scheduledBaseStatus(agent, &runs)
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

	runs, err = r.pruneScheduledHistory(ctx, policy, runs)
	if err != nil {
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
		recoverScheduledRunStatus(&nextStatus, existing, due, runs.latestDue)
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
	scheduledrun.SetMetadata(run, due, sequence)

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
		recoverScheduledRunStatus(&nextStatus, existing, due, runs.latestDue)
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
	children, err := r.ownedRuns(ctx, agent)
	if err != nil {
		return observedScheduledRuns{}, err
	}

	return summarizeScheduledRuns(children), nil
}

func summarizeScheduledRuns(items []kontextv1alpha1.AgentRun) observedScheduledRuns {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
	observed := observedScheduledRuns{
		items:  items,
		bySlot: make(map[string]*kontextv1alpha1.AgentRun),
	}
	for i := range observed.items {
		run := &observed.items[i]
		if !run.Status.Phase.IsTerminal() {
			observed.hasActive = true
		}

		sequence, ok := scheduledrun.Sequence(run)
		if ok && sequence > observed.maxCount {
			observed.maxCount = sequence
		}
		slot, ok := scheduledrun.Slot(run)
		if !ok {
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
	return observed
}

func scheduledBaseStatus(
	agent *kontextv1alpha1.Agent,
	runs *observedScheduledRuns,
) kontextv1alpha1.AgentStatus {
	next := kontextv1alpha1.AgentStatus{
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
	// LastRunName is a live reference to the newest retained child, while
	// LastScheduleTime is historical progress and may outlive a pruned child.
	if runs.latest != nil {
		next.LastRunName = runs.latest.Name
		if next.LastScheduleTime == nil || runs.latestDue.After(next.LastScheduleTime.Time) {
			next.LastScheduleTime = timePtr(runs.latestDue)
		}
	}
	return next
}

func recoverScheduledRunStatus(
	next *kontextv1alpha1.AgentStatus,
	run *kontextv1alpha1.AgentRun,
	slot time.Time,
	latestRetainedSlot time.Time,
) {
	if sequence, ok := scheduledrun.Sequence(run); ok && sequence > next.RunsCreated {
		next.RunsCreated = sequence
	}
	if latestRetainedSlot.IsZero() || !slot.Before(latestRetainedSlot) {
		next.LastRunName = run.Name
	}
	if next.LastScheduleTime == nil || slot.After(next.LastScheduleTime.Time) {
		next.LastScheduleTime = timePtr(slot)
	}
}

func (r *AgentReconciler) verifyScheduledRun(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	runName string,
	slot time.Time,
) (*kontextv1alpha1.AgentRun, error) {
	existing, accepted, err := r.verifyExistingOwnedRun(
		ctx,
		agent,
		runName,
		func(run *kontextv1alpha1.AgentRun) bool {
			return scheduledrun.RepresentsSlot(run, slot)
		},
	)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if !accepted {
		return nil, fmt.Errorf(
			"scheduled run name collision: AgentRun %s/%s does not represent slot %s for Agent %s/%s",
			existing.Namespace,
			existing.Name,
			slot.UTC().Format(time.RFC3339),
			agent.Namespace,
			agent.Name,
		)
	}
	return existing, nil
}

func (r *AgentReconciler) pruneScheduledHistory(
	ctx context.Context,
	policy scheduler.Policy,
	runs observedScheduledRuns,
) (observedScheduledRuns, error) {
	successful := make([]*kontextv1alpha1.AgentRun, 0)
	failed := make([]*kontextv1alpha1.AgentRun, 0)
	for i := range runs.items {
		run := &runs.items[i]
		switch run.Status.Phase {
		case kontextv1alpha1.AgentRunPhaseSucceeded:
			successful = append(successful, run)
		case kontextv1alpha1.AgentRunPhaseFailed, kontextv1alpha1.AgentRunPhaseBudgetExceeded:
			failed = append(failed, run)
		}
	}
	deleted := make(map[string]struct{})
	if err := r.pruneScheduledGroup(
		ctx,
		successful,
		policy.SuccessfulRunsHistoryLimit,
		deleted,
	); err != nil {
		return observedScheduledRuns{}, err
	}
	if err := r.pruneScheduledGroup(ctx, failed, policy.FailedRunsHistoryLimit, deleted); err != nil {
		return observedScheduledRuns{}, err
	}

	retained := make([]kontextv1alpha1.AgentRun, 0, len(runs.items)-len(deleted))
	for i := range runs.items {
		if _, wasDeleted := deleted[runs.items[i].Name]; !wasDeleted {
			retained = append(retained, runs.items[i])
		}
	}
	observed := summarizeScheduledRuns(retained)
	if runs.maxCount > observed.maxCount {
		observed.maxCount = runs.maxCount
	}
	return observed, nil
}

func (r *AgentReconciler) pruneScheduledGroup(
	ctx context.Context,
	runs []*kontextv1alpha1.AgentRun,
	limit int32,
	deleted map[string]struct{},
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
		deleted[runs[i].Name] = struct{}{}
	}
	return nil
}

func scheduledRunSortTime(run *kontextv1alpha1.AgentRun) time.Time {
	if slot, ok := scheduledrun.Slot(run); ok {
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
