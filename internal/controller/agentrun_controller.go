package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/conditions"
	"github.com/kontext-dev/kontext/internal/podbuilder"
	"github.com/kontext-dev/kontext/internal/status"
)

// AgentRunReconciler reconciles an AgentRun object.
type AgentRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kontext.dev,resources=agentruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kontext.dev,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run kontextv1alpha1.AgentRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	podName := run.Status.PodName
	if podName == "" {
		podName = podbuilder.PodNameForRun(run.Name)
	}

	pod := &corev1.Pod{}
	podKey := client.ObjectKey{Namespace: run.Namespace, Name: podName}
	err := r.Get(ctx, podKey, pod)
	if apierrors.IsNotFound(err) {
		return r.reconcileMissingPod(ctx, &run, podName)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	wallclockResult, done, err := r.enforceWallclock(ctx, &run, pod)
	if err != nil || done {
		return wallclockResult, err
	}

	observationResult, err := r.syncPodObservation(ctx, &run, pod, podName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if wallclockResult.RequeueAfter > 0 {
		return wallclockResult, nil
	}
	return observationResult, nil
}

func (r *AgentRunReconciler) reconcileMissingPod(ctx context.Context, run *kontextv1alpha1.AgentRun, podName string) (ctrl.Result, error) {
	if status.IsTerminalPhase(run.Status.Phase) {
		return ctrl.Result{}, nil
	}

	if run.Status.PodName != "" {
		return r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
			next.Phase = kontextv1alpha1.AgentRunPhaseFailed
			next.PodName = podName
			next.Message = "Pod lost before run completed."
			next.CompletionTime = nowPtr()
			next.Conditions = conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseFailed, next.Conditions)
		})
	}

	pod := podbuilder.BuildPod(run)
	if err := controllerutil.SetControllerReference(run, pod, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	return r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = kontextv1alpha1.AgentRunPhasePending
		next.PodName = podName
		next.Message = "Agent run pod requested."
		next.Conditions = conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhasePending, next.Conditions)
	})
}

func (r *AgentRunReconciler) syncPodObservation(ctx context.Context, run *kontextv1alpha1.AgentRun, pod *corev1.Pod, podName string) (ctrl.Result, error) {
	observation := status.ObservePod(pod)

	_, err := r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = observation.Phase
		next.PodName = podName
		next.Message = observation.Message
		if observation.Result != "" {
			next.Result = observation.Result
		}
		if observation.Usage != nil {
			next.Usage = observation.Usage
		}
		if next.StartTime == nil && observation.Phase == kontextv1alpha1.AgentRunPhaseRunning {
			next.StartTime = nowPtr()
		}
		if status.IsTerminalPhase(observation.Phase) && next.CompletionTime == nil {
			next.CompletionTime = nowPtr()
		}
		next.Conditions = conditions.ForAgentRunPhase(observation.Phase, next.Conditions)
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *AgentRunReconciler) enforceWallclock(ctx context.Context, run *kontextv1alpha1.AgentRun, pod *corev1.Pod) (ctrl.Result, bool, error) {
	if run.Spec.Budget == nil || run.Spec.Budget.Wallclock == "" {
		return ctrl.Result{}, false, nil
	}
	if status.IsTerminalPhase(run.Status.Phase) {
		return ctrl.Result{}, false, nil
	}

	wallclock := status.ParseWallclockDetailed(run.Spec.Budget.Wallclock, 300)
	if !wallclock.Valid {
		_, err := r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
			next.Conditions = conditions.Merge(next.Conditions, conditions.BudgetConfigured(false, wallclock.Warning))
		})
		if err != nil {
			return ctrl.Result{}, false, err
		}
		return ctrl.Result{}, false, nil
	}

	if pod.Status.Phase != corev1.PodRunning {
		return ctrl.Result{}, false, nil
	}

	limit := wallclock.Duration
	startedAt := run.Status.StartTime
	if startedAt == nil {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Running != nil {
				startedAt = &cs.State.Running.StartedAt
				break
			}
		}
	}
	if startedAt == nil {
		return ctrl.Result{}, false, nil
	}

	elapsed := time.Since(startedAt.Time)
	if elapsed <= limit {
		remaining := limit - elapsed
		return ctrl.Result{RequeueAfter: remaining + time.Second}, false, nil
	}

	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, false, err
	}

	_, err := r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = kontextv1alpha1.AgentRunPhaseBudgetExceeded
		next.PodName = pod.Name
		next.Message = fmt.Sprintf("Wallclock budget exceeded after %s.", limit)
		if next.StartTime == nil {
			next.StartTime = run.Status.StartTime
		}
		next.CompletionTime = nowPtr()
		next.Conditions = conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseBudgetExceeded, next.Conditions)
	})
	if err != nil {
		return ctrl.Result{}, false, err
	}
	return ctrl.Result{}, true, nil
}

func (r *AgentRunReconciler) patchRunStatus(ctx context.Context, run *kontextv1alpha1.AgentRun, mutate func(*kontextv1alpha1.AgentRunStatus)) (ctrl.Result, error) {
	if err := patchStatus(ctx, r.Client, run, func() {
		next := run.Status.DeepCopy()
		mutate(next)
		run.Status = *next
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kontextv1alpha1.AgentRun{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
