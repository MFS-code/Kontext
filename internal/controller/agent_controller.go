package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/conditions"
	"github.com/kontext-dev/kontext/internal/podbuilder"
	"github.com/kontext-dev/kontext/internal/runtimepolicy"
	"github.com/kontext-dev/kontext/internal/status"
	"github.com/kontext-dev/kontext/internal/util"
)

const (
	defaultBackoffInitial = 5
	defaultBackoffMax     = 60
)

// AgentReconciler reconciles an Agent object.
type AgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
		return r.setAgentStatus(ctx, &agent, agent.Status, false, conditions.UnsupportedMode(string(agent.Spec.Mode))...)
	default:
		return r.setAgentStatus(ctx, &agent, agent.Status, false, conditions.InvalidMode(string(agent.Spec.Mode))...)
	}
}

func (r *AgentReconciler) reconcileService(ctx context.Context, agent *kontextv1alpha1.Agent) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if agent.Spec.Goal == "" {
		return r.setAgentStatus(ctx, agent, agent.Status, false, metav1.Condition{
			Type:    conditions.Ready,
			Status:  metav1.ConditionFalse,
			Reason:  "MissingGoal",
			Message: "Service agents require spec.goal.",
		})
	}

	currentRun, missingCurrentRun, err := r.getCurrentRun(ctx, agent)
	if err != nil {
		return ctrl.Result{}, err
	}

	if missingCurrentRun {
		logger.Info("current service run missing; minting replacement", "agent", agent.Name, "run", agent.Status.CurrentRunName)
	}

	if currentRun != nil && !status.IsTerminalPhase(currentRun.Status.Phase) {
		return r.setAgentStatus(ctx, agent, kontextv1alpha1.AgentStatus{
			CurrentRunName:     currentRun.Name,
			LastRunName:        agent.Status.LastRunName,
			RunsCreated:        agent.Status.RunsCreated,
			Restarts:           agent.Status.Restarts,
			ObservedGeneration: agent.Generation,
		}, false, metav1.Condition{
			Type:    conditions.Ready,
			Status:  metav1.ConditionTrue,
			Reason:  "RunActive",
			Message: fmt.Sprintf("Service run %s is active.", currentRun.Name),
		}, metav1.Condition{
			Type:    conditions.Progressing,
			Status:  metav1.ConditionFalse,
			Reason:  "RunActive",
			Message: "Live service run is active.",
		})
	}

	if currentRun != nil && status.IsTerminalPhase(currentRun.Status.Phase) {
		delay := r.backoffDelay(*agent)
		if since := timeSinceCompletion(currentRun); since < delay {
			logger.Info("waiting before service recast", "delay", delay-since, "run", currentRun.Name)
			return ctrl.Result{RequeueAfter: delay - since}, nil
		}
	}

	runName := fmt.Sprintf("%s-%d", agent.Name, agent.Status.RunsCreated+1)
	run := r.buildServiceRun(agent, runName)
	if err := controllerutil.SetControllerReference(agent, run, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	created := true
	if err := r.Create(ctx, run); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		created = false
		var existing kontextv1alpha1.AgentRun
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: agent.Namespace,
			Name:      runName,
		}, &existing); err != nil {
			return ctrl.Result{}, err
		}
		run = &existing
	}

	nextStatus := kontextv1alpha1.AgentStatus{
		CurrentRunName:     run.Name,
		LastRunName:        agent.Status.CurrentRunName,
		RunsCreated:        agent.Status.RunsCreated,
		Restarts:           agent.Status.Restarts,
		ObservedGeneration: agent.Generation,
	}
	if created {
		nextStatus.RunsCreated = agent.Status.RunsCreated + 1
		if currentRun != nil {
			nextStatus.Restarts = agent.Status.Restarts + 1
		}
	}

	return r.setAgentStatus(ctx, agent, nextStatus, true, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionFalse,
		Reason:  "Recasting",
		Message: fmt.Sprintf("Minted service run %s.", run.Name),
	}, metav1.Condition{
		Type:    conditions.Progressing,
		Status:  metav1.ConditionTrue,
		Reason:  "Recasting",
		Message: "Service run is being created.",
	})
}

func (r *AgentReconciler) getCurrentRun(ctx context.Context, agent *kontextv1alpha1.Agent) (*kontextv1alpha1.AgentRun, bool, error) {
	if agent.Status.CurrentRunName == "" {
		return nil, false, nil
	}
	var run kontextv1alpha1.AgentRun
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: agent.Namespace,
		Name:      agent.Status.CurrentRunName,
	}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return &run, false, nil
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
			Tools:    util.CloneSlice(agent.Spec.Tools),
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

func (r *AgentReconciler) backoffDelay(agent kontextv1alpha1.Agent) time.Duration {
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

	attempt := agent.Status.Restarts
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(initial) * time.Second
	for i := int32(1); i < attempt; i++ {
		delay *= 2
	}
	maxDelay := time.Duration(maximum) * time.Second
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func timeSinceCompletion(run *kontextv1alpha1.AgentRun) time.Duration {
	if run.Status.CompletionTime != nil {
		return time.Since(run.Status.CompletionTime.Time)
	}
	return 0
}

func (r *AgentReconciler) setAgentStatus(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	next kontextv1alpha1.AgentStatus,
	requeue bool,
	updates ...metav1.Condition,
) (ctrl.Result, error) {
	next.Conditions = conditions.Merge(agent.Status.Conditions, updates...)
	if err := patchStatus(ctx, r.Client, agent, func() {
		agent.Status = next
	}); err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kontextv1alpha1.Agent{}).
		Owns(&kontextv1alpha1.AgentRun{}).
		Complete(r)
}
