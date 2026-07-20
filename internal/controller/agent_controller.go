package controller

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/runtimepolicy"
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
	case kontextv1alpha1.AgentModeTask, kontextv1alpha1.AgentModeScheduled:
		return ctrl.Result{}, r.setAgentStatus(ctx, &agent, agent.Status, conditions.UnsupportedMode(string(agent.Spec.Mode))...)
	default:
		return ctrl.Result{}, r.setAgentStatus(ctx, &agent, agent.Status, conditions.InvalidMode(string(agent.Spec.Mode))...)
	}
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
	run := r.buildServiceRun(agent, runName)
	if err := controllerutil.SetControllerReference(agent, run, r.Scheme); err != nil {
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

func (r *AgentReconciler) buildServiceRun(agent *kontextv1alpha1.Agent, runName string) *kontextv1alpha1.AgentRun {
	provider := runtimepolicy.NormalizeProvider(agent.Spec.Provider)
	agentSpec := agent.Spec.DeepCopy()

	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: agent.Namespace,
			Labels: map[string]string{
				podbuilder.LabelAgentName: agent.Name,
			},
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef: &kontextv1alpha1.AgentRef{Name: agent.Name},
			Goal:     agent.Spec.Goal,
			Provider: provider,
			Model:    agent.Spec.Model,
			Tools:    slices.Clone(agent.Spec.Tools),
			Budget:   agent.Spec.Budget,
			Runtime:  *agent.Spec.Runtime.DeepCopy(),
			Env:      agentSpec.Env,
		},
	}
	if agent.Spec.SecretRef != nil {
		run.Spec.SecretRef = &kontextv1alpha1.SecretRef{Name: agent.Spec.SecretRef.Name}
	}
	if agent.Spec.KnowledgeConfigMapRef != nil {
		run.Spec.KnowledgeConfigMapRef = &kontextv1alpha1.ConfigMapRef{Name: agent.Spec.KnowledgeConfigMapRef.Name}
	}
	if agent.Spec.ServiceAccountName != "" {
		run.Spec.ServiceAccountName = agent.Spec.ServiceAccountName
	}
	return run
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
	setStatusConditions(&next.Conditions, agent.Generation, updates...)
	if err := patchStatus(ctx, r.Client, agent, func() {
		agent.Status = next
	}); err != nil {
		return err
	}
	return nil
}

// SetupWithManager sets up the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kontextv1alpha1.Agent{}).
		Owns(&kontextv1alpha1.AgentRun{}).
		Complete(r)
}
