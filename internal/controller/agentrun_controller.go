package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/status"
)

// AgentRunReconciler reconciles an AgentRun object.
type AgentRunReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ReporterImage string
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
	podErr := r.Get(ctx, podKey, pod)
	podMissing := apierrors.IsNotFound(podErr)
	if podErr != nil && !podMissing {
		return ctrl.Result{}, podErr
	}

	var wallclockLimit *time.Duration
	if !status.IsTerminalPhase(run.Status.Phase) {
		parsedLimit, budgetErr := parseWallclockBudget(run.Spec.Budget)
		if budgetErr != nil {
			return r.failInvalidWallclock(ctx, &run, pod, !podMissing, budgetErr)
		}
		wallclockLimit = parsedLimit
	}

	if podMissing {
		return r.reconcileMissingPod(ctx, &run, podName)
	}

	wallclockResult, done, err := r.enforceWallclock(ctx, &run, pod, wallclockLimit)
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
			setStatusConditions(
				&next.Conditions,
				run.Generation,
				conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseFailed)...,
			)
		})
	}

	pod, err := podbuilder.BuildPodWithConfig(run, podbuilder.Config{
		ReporterImage: r.ReporterImage,
	})
	if err != nil {
		return r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
			next.Phase = kontextv1alpha1.AgentRunPhaseFailed
			next.Message = fmt.Sprintf("Agent run configuration is invalid: %v.", err)
			next.CompletionTime = nowPtr()
			setStatusConditions(
				&next.Conditions,
				run.Generation,
				conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseFailed)...,
			)
		})
	}
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
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhasePending)...,
		)
	})
}

func (r *AgentRunReconciler) syncPodObservation(ctx context.Context, run *kontextv1alpha1.AgentRun, pod *corev1.Pod, podName string) (ctrl.Result, error) {
	observation := status.ObservePod(pod)

	_, err := r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = observation.Phase
		next.PodName = podName
		next.Message = observation.Message
		if observation.Output != nil {
			next.Output = observation.Output
			next.Result = observation.Result
		}
		if observation.Usage != nil {
			next.Usage = observation.Usage
		}
		if next.StartTime == nil && observation.Phase == kontextv1alpha1.AgentRunPhaseRunning {
			next.StartTime = runtimeContainerStartedAt(pod)
			if next.StartTime == nil {
				next.StartTime = nowPtr()
			}
		}
		if status.IsTerminalPhase(observation.Phase) && next.CompletionTime == nil {
			next.CompletionTime = nowPtr()
		}
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			conditions.ForAgentRunPhase(observation.Phase)...,
		)
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func parseWallclockBudget(budget *kontextv1alpha1.BudgetSpec) (*time.Duration, error) {
	if budget == nil || budget.Wallclock == "" {
		return nil, nil
	}

	limit, err := time.ParseDuration(budget.Wallclock)
	if err != nil {
		return nil, fmt.Errorf("invalid wallclock budget %q: %w", budget.Wallclock, err)
	}
	if limit <= 0 {
		return nil, fmt.Errorf(
			"invalid wallclock budget %q: duration must be positive",
			budget.Wallclock,
		)
	}
	return &limit, nil
}

func (r *AgentRunReconciler) failInvalidWallclock(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	pod *corev1.Pod,
	podExists bool,
	cause error,
) (ctrl.Result, error) {
	if podExists {
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	return r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = kontextv1alpha1.AgentRunPhaseFailed
		if podExists {
			next.PodName = pod.Name
		}
		next.Message = fmt.Sprintf("Agent run configuration is invalid: %v.", cause)
		next.CompletionTime = nowPtr()
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseFailed)...,
		)
	})
}

func (r *AgentRunReconciler) enforceWallclock(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	pod *corev1.Pod,
	limit *time.Duration,
) (ctrl.Result, bool, error) {
	if run.Status.Phase == kontextv1alpha1.AgentRunPhaseBudgetExceeded {
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{}, true, nil
	}
	if limit == nil {
		return ctrl.Result{}, false, nil
	}
	if status.IsTerminalPhase(run.Status.Phase) {
		return ctrl.Result{}, false, nil
	}

	if pod.Status.Phase != corev1.PodRunning {
		return ctrl.Result{}, false, nil
	}

	startedAt := runtimeContainerStartedAt(pod)
	if startedAt == nil {
		startedAt = run.Status.StartTime
	}
	if startedAt == nil {
		return ctrl.Result{}, false, nil
	}

	elapsed := time.Since(startedAt.Time)
	if elapsed <= *limit {
		remaining := *limit - elapsed
		return ctrl.Result{RequeueAfter: remaining + time.Second}, false, nil
	}

	_, err := r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = kontextv1alpha1.AgentRunPhaseBudgetExceeded
		next.PodName = pod.Name
		next.Message = fmt.Sprintf("Wallclock budget exceeded after %s.", *limit)
		recordedStart := *startedAt
		next.StartTime = &recordedStart
		next.CompletionTime = nowPtr()
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseBudgetExceeded)...,
		)
	})
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{}, true, nil
}

func runtimeContainerStartedAt(pod *corev1.Pod) *metav1.Time {
	for index := range pod.Status.ContainerStatuses {
		containerStatus := &pod.Status.ContainerStatuses[index]
		if containerStatus.Name == podbuilder.RuntimeContainerName &&
			containerStatus.State.Running != nil {
			startedAt := containerStatus.State.Running.StartedAt
			return &startedAt
		}
	}
	return nil
}

func (r *AgentRunReconciler) patchRunStatus(ctx context.Context, run *kontextv1alpha1.AgentRun, mutate func(*kontextv1alpha1.AgentRunStatus)) (ctrl.Result, error) {
	if err := patchStatus(ctx, r.Client, run, func() {
		mutate(&run.Status)
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
