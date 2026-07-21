package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/runfactory"
	"github.com/MFS-code/Kontext/internal/scheduler"
	"github.com/MFS-code/Kontext/internal/status"
)

const (
	defaultBackoffInitial = 5
	defaultBackoffMax     = 60
	maxRunSuffix          = int32(1<<31 - 1)
)

// AgentReconciler reconciles an Agent object.
type AgentReconciler struct {
	client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Clock     scheduler.Clock
}

// +kubebuilder:rbac:groups=kontext.dev,resources=agents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kontext.dev,resources=agents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kontext.dev,resources=agentruns,verbs=get;list;watch;create;update;patch;delete

func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var agent kontextv1alpha1.Agent
	if err := r.Get(ctx, req.NamespacedName, &agent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch agent.Spec.Mode {
	case kontextv1alpha1.AgentModeService:
		return r.reconcileService(ctx, &agent)
	case kontextv1alpha1.AgentModeScheduled:
		return r.reconcileScheduled(ctx, &agent)
	case kontextv1alpha1.AgentModeTask:
		return r.reconcileTask(ctx, &agent)
	default:
		return ctrl.Result{}, r.setAgentStatus(ctx, &agent, agent.Status, conditions.InvalidMode(string(agent.Spec.Mode))...)
	}
}

func (r *AgentReconciler) reconcileTask(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
) (ctrl.Result, error) {
	var children kontextv1alpha1.AgentRunList
	if err := r.List(ctx, &children, client.InNamespace(agent.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	var newest *kontextv1alpha1.AgentRun
	var retained int32
	for i := range children.Items {
		run := &children.Items[i]
		if !metav1.IsControlledBy(run, agent) {
			continue
		}
		if retained == maxRunSuffix {
			return ctrl.Result{}, fmt.Errorf(
				"Task Agent %s/%s owns more than %d retained AgentRuns",
				agent.Namespace,
				agent.Name,
				maxRunSuffix,
			)
		}
		retained++
		if newest == nil ||
			run.CreationTimestamp.After(newest.CreationTimestamp.Time) ||
			run.CreationTimestamp.Equal(&newest.CreationTimestamp) && run.Name > newest.Name {
			newest = run
		}
	}

	next := kontextv1alpha1.AgentStatus{
		RunsCreated:        retained,
		ObservedGeneration: agent.Generation,
	}
	if newest != nil {
		next.LastRunName = newest.Name
	}

	if err := runfactory.ValidateTask(agent); err != nil {
		return ctrl.Result{}, r.setAgentStatus(ctx, agent, next, metav1.Condition{
			Type:    conditions.Ready,
			Status:  metav1.ConditionFalse,
			Reason:  "InvalidTemplate",
			Message: err.Error(),
		}, metav1.Condition{
			Type:    conditions.Progressing,
			Status:  metav1.ConditionFalse,
			Reason:  "InvalidTemplate",
			Message: "Task invocations are blocked until the template is valid.",
		})
	}

	return ctrl.Result{}, r.setAgentStatus(ctx, agent, next, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionTrue,
		Reason:  "TemplateReady",
		Message: "Task template is ready for invocations.",
	}, metav1.Condition{
		Type:    conditions.Progressing,
		Status:  metav1.ConditionFalse,
		Reason:  "Idle",
		Message: "Task Agents run only when an AgentRun is created.",
	})
}

func (r *AgentReconciler) reconcileService(ctx context.Context, agent *kontextv1alpha1.Agent) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	runs, err := r.observeServiceRuns(ctx, agent)
	if err != nil {
		return ctrl.Result{}, err
	}

	if agent.Spec.Goal == "" {
		return ctrl.Result{}, r.setAgentStatus(ctx, agent, runs.status(agent.Generation), metav1.Condition{
			Type:    conditions.Ready,
			Status:  metav1.ConditionFalse,
			Reason:  "MissingGoal",
			Message: "Service agents require spec.goal.",
		})
	}

	if runs.current != nil && !status.IsTerminalPhase(runs.current.Status.Phase) {
		return ctrl.Result{}, r.setAgentStatus(ctx, agent, runs.status(agent.Generation), metav1.Condition{
			Type:    conditions.Ready,
			Status:  metav1.ConditionTrue,
			Reason:  "RunActive",
			Message: fmt.Sprintf("Service run %s is active.", runs.current.Name),
		}, metav1.Condition{
			Type:    conditions.Progressing,
			Status:  metav1.ConditionFalse,
			Reason:  "RunActive",
			Message: "Live service run is active.",
		})
	}

	if runs.current != nil && status.IsTerminalPhase(runs.current.Status.Phase) &&
		runs.current.Status.CompletionTime != nil {
		delay := r.backoffDelay(*agent, runs.maxSuffix-1)
		if since := time.Since(runs.current.Status.CompletionTime.Time); since < delay {
			logger.Info("waiting before service recast", "delay", delay-since, "run", runs.current.Name)
			if err := r.setAgentStatus(ctx, agent, runs.status(agent.Generation)); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: delay - since}, nil
		}
	}

	if runs.maxSuffix == maxRunSuffix {
		return ctrl.Result{}, fmt.Errorf("service run suffix exhausted for Agent %s/%s", agent.Namespace, agent.Name)
	}
	nextSuffix := runs.maxSuffix + 1
	runName := fmt.Sprintf("%s-%d", agent.Name, nextSuffix)
	run, err := runfactory.NewForAgent(agent, runName, agent.Spec.Goal, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, run); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		return r.handleServiceRunAlreadyExists(ctx, agent, runName)
	}

	nextStatus := kontextv1alpha1.AgentStatus{
		CurrentRunName:     run.Name,
		RunsCreated:        nextSuffix,
		Restarts:           nextSuffix - 1,
		ObservedGeneration: agent.Generation,
	}
	if runs.current != nil {
		nextStatus.LastRunName = runs.current.Name
	}

	if err := r.setAgentStatus(ctx, agent, nextStatus, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionFalse,
		Reason:  "Recasting",
		Message: fmt.Sprintf("Minted service run %s.", run.Name),
	}, metav1.Condition{
		Type:    conditions.Progressing,
		Status:  metav1.ConditionTrue,
		Reason:  "Recasting",
		Message: "Service run is being created.",
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *AgentReconciler) handleServiceRunAlreadyExists(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	runName string,
) (ctrl.Result, error) {
	if r.APIReader == nil {
		return ctrl.Result{}, fmt.Errorf("cannot verify existing AgentRun %s/%s: APIReader is not configured", agent.Namespace, runName)
	}

	var existing kontextv1alpha1.AgentRun
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: runName}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	if metav1.IsControlledBy(&existing, agent) {
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, fmt.Errorf(
		"service run name collision: AgentRun %s/%s is not controlled by Agent %s/%s",
		existing.Namespace,
		existing.Name,
		agent.Namespace,
		agent.Name,
	)
}

type observedServiceRuns struct {
	current   *kontextv1alpha1.AgentRun
	previous  *kontextv1alpha1.AgentRun
	maxSuffix int32
}

func (r *AgentReconciler) observeServiceRuns(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
) (observedServiceRuns, error) {
	var children kontextv1alpha1.AgentRunList
	if err := r.List(
		ctx,
		&children,
		client.InNamespace(agent.Namespace),
		client.MatchingLabels{podbuilder.LabelAgentName: agent.Name},
	); err != nil {
		return observedServiceRuns{}, err
	}

	var observed observedServiceRuns
	var previousSuffix int32
	for i := range children.Items {
		run := &children.Items[i]
		if !metav1.IsControlledBy(run, agent) {
			continue
		}
		suffix, ok := serviceRunSuffix(agent.Name, run.Name)
		if !ok {
			continue
		}
		switch {
		case suffix > observed.maxSuffix:
			observed.previous = observed.current
			previousSuffix = observed.maxSuffix
			observed.current = run
			observed.maxSuffix = suffix
		case suffix > previousSuffix:
			observed.previous = run
			previousSuffix = suffix
		}
	}
	return observed, nil
}

func serviceRunSuffix(agentName, runName string) (int32, bool) {
	prefix := agentName + "-"
	if !strings.HasPrefix(runName, prefix) {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimPrefix(runName, prefix), 10, 32)
	if err != nil || value < 1 {
		return 0, false
	}
	suffix := int32(value)
	return suffix, runName == fmt.Sprintf("%s-%d", agentName, suffix)
}

func (runs observedServiceRuns) status(generation int64) kontextv1alpha1.AgentStatus {
	// Run suffixes form a monotonic creation sequence, so deleting old runs
	// does not reduce the historical creation and restart counters.
	next := kontextv1alpha1.AgentStatus{
		RunsCreated:        runs.maxSuffix,
		ObservedGeneration: generation,
	}
	if runs.maxSuffix > 0 {
		next.Restarts = runs.maxSuffix - 1
	}
	if runs.current != nil {
		next.CurrentRunName = runs.current.Name
	}
	if runs.previous != nil {
		next.LastRunName = runs.previous.Name
	}
	return next
}

func (r *AgentReconciler) backoffDelay(agent kontextv1alpha1.Agent, restarts int32) time.Duration {
	initial := int32(defaultBackoffInitial)
	maximum := int32(defaultBackoffMax)
	if agent.Spec.Backoff != nil {
		if agent.Spec.Backoff.InitialSeconds > 0 {
			initial = agent.Spec.Backoff.InitialSeconds
		}
		if agent.Spec.Backoff.MaxSeconds > 0 {
			maximum = agent.Spec.Backoff.MaxSeconds
		}
	}

	attempt := restarts
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(initial) * time.Second
	maxDelay := time.Duration(maximum) * time.Second
	if delay >= maxDelay {
		return maxDelay
	}
	for i := int32(1); i < attempt; i++ {
		if delay > maxDelay-delay {
			return maxDelay
		}
		delay *= 2
		if delay >= maxDelay {
			return maxDelay
		}
	}
	return delay
}

func (r *AgentReconciler) setAgentStatus(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	next kontextv1alpha1.AgentStatus,
	updates ...metav1.Condition,
) error {
	next.Conditions = agent.Status.Conditions
	for i := range updates {
		updates[i].LastTransitionTime = metav1.NewTime(r.now())
	}
	setStatusConditions(&next.Conditions, agent.Generation, updates...)
	if err := patchStatus(ctx, r.Client, agent, func() {
		agent.Status = next
	}); err != nil {
		return err
	}
	return nil
}

func (r *AgentReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

// SetupWithManager sets up the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kontextv1alpha1.Agent{}).
		Owns(&kontextv1alpha1.AgentRun{}).
		Complete(r)
}
