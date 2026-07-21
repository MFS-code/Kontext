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
	"github.com/MFS-code/Kontext/internal/scheduler"
	"github.com/MFS-code/Kontext/internal/status"
)

// AgentRunReconciler reconciles an AgentRun object.
type AgentRunReconciler struct {
	client.Client
	APIReader     client.Reader
	Scheme        *runtime.Scheme
	ReporterImage string
	Clock         scheduler.Clock
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

	if run.Status.Phase.IsTerminal() {
		if run.Status.Phase == kontextv1alpha1.AgentRunPhaseBudgetExceeded {
			return ctrl.Result{}, r.deleteBudgetExceededPod(ctx, &run)
		}
		return ctrl.Result{}, nil
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
	if podMissing && run.Status.PodName == "" {
		legacyPodName := podbuilder.LegacyPodNameForRun(run.Name)
		if legacyPodName != podName {
			legacyPod := &corev1.Pod{}
			legacyPodKey := client.ObjectKey{Namespace: run.Namespace, Name: legacyPodName}
			legacyPodErr := r.Get(ctx, legacyPodKey, legacyPod)
			switch {
			case legacyPodErr == nil && metav1.IsControlledBy(legacyPod, &run):
				pod = legacyPod
				podName = legacyPodName
				podMissing = false
			case legacyPodErr != nil && !apierrors.IsNotFound(legacyPodErr):
				return ctrl.Result{}, legacyPodErr
			}
		}
	}
	if podMissing && run.Status.PodName != "" {
		if r.APIReader == nil {
			return ctrl.Result{}, fmt.Errorf(
				"cannot verify missing Pod %s/%s: APIReader is not configured",
				run.Namespace,
				podName,
			)
		}
		livePod := &corev1.Pod{}
		if err := r.APIReader.Get(ctx, podKey, livePod); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		} else {
			pod = livePod
			podMissing = false
		}
	}
	if !podMissing && !metav1.IsControlledBy(pod, &run) {
		return r.failPodNameCollision(ctx, &run, pod)
	}

	wallclockLimit, budgetErr := parseWallclockBudget(run.Spec.Budget)
	if budgetErr != nil {
		return r.failInvalidWallclock(ctx, &run, pod, !podMissing, budgetErr)
	}

	if podMissing {
		return r.reconcileMissingPod(ctx, &run, podName)
	}

	wallclockResult, err := r.enforceWallclock(ctx, &run, pod, wallclockLimit)
	if err != nil || run.Status.Phase.IsTerminal() {
		return wallclockResult, err
	}

	if err := r.syncPodObservation(ctx, &run, pod, podName); err != nil {
		return ctrl.Result{}, err
	}
	return wallclockResult, nil
}

func (r *AgentRunReconciler) deleteBudgetExceededPod(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
) error {
	if r.APIReader == nil {
		return fmt.Errorf(
			"cannot delete Pod for budget-exceeded AgentRun %s/%s: APIReader is not configured",
			run.Namespace,
			run.Name,
		)
	}

	podName := run.Status.PodName
	if podName == "" {
		podName = podbuilder.PodNameForRun(run.Name)
	}
	pod := &corev1.Pod{}
	podKey := client.ObjectKey{Namespace: run.Namespace, Name: podName}
	if err := r.APIReader.Get(ctx, podKey, pod); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(pod, run) {
		return nil
	}
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *AgentRunReconciler) failPodNameCollision(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	pod *corev1.Pod,
) (ctrl.Result, error) {
	message := fmt.Sprintf(
		"Pod name collision: Pod %s/%s is not controlled by AgentRun %s/%s.",
		pod.Namespace,
		pod.Name,
		run.Namespace,
		run.Name,
	)
	return ctrl.Result{}, r.transitionRun(
		ctx,
		run,
		kontextv1alpha1.AgentRunPhaseFailed,
		message,
		func(next *kontextv1alpha1.AgentRunStatus) {
			next.PodName = pod.Name
			next.Result = ""
			next.Output = nil
			next.Usage = nil
			next.StartTime = nil
		},
	)
}

func (r *AgentRunReconciler) reconcileMissingPod(ctx context.Context, run *kontextv1alpha1.AgentRun, podName string) (ctrl.Result, error) {
	if run.Status.PodName != "" {
		return ctrl.Result{}, r.transitionRun(
			ctx,
			run,
			kontextv1alpha1.AgentRunPhaseFailed,
			"Pod lost before run completed.",
			func(next *kontextv1alpha1.AgentRunStatus) {
				next.PodName = podName
			},
		)
	}

	pod, err := podbuilder.BuildPodWithConfig(run, podbuilder.Config{
		ReporterImage: r.ReporterImage,
	})
	if err != nil {
		return ctrl.Result{}, r.transitionRun(
			ctx,
			run,
			kontextv1alpha1.AgentRunPhaseFailed,
			fmt.Sprintf("Agent run configuration is invalid: %v.", err),
			nil,
		)
	}
	if err := controllerutil.SetControllerReference(run, pod, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.transitionRun(
		ctx,
		run,
		kontextv1alpha1.AgentRunPhasePending,
		"Agent run pod requested.",
		func(next *kontextv1alpha1.AgentRunStatus) {
			next.PodName = podName
		},
	)
}

func (r *AgentRunReconciler) syncPodObservation(ctx context.Context, run *kontextv1alpha1.AgentRun, pod *corev1.Pod, podName string) error {
	observation := status.ObservePod(pod)

	err := r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
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
			next.StartTime = observation.StartedAt
			if next.StartTime == nil {
				next.StartTime = r.nowPtr()
			}
		}
		if observation.Phase.IsTerminal() && next.CompletionTime == nil {
			next.CompletionTime = r.nowPtr()
		}
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			conditions.ForAgentRunPhase(observation.Phase)...,
		)
	})
	if err != nil {
		return err
	}

	return nil
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

	return ctrl.Result{}, r.transitionRun(
		ctx,
		run,
		kontextv1alpha1.AgentRunPhaseFailed,
		fmt.Sprintf("Agent run configuration is invalid: %v.", cause),
		func(next *kontextv1alpha1.AgentRunStatus) {
			if podExists {
				next.PodName = pod.Name
			}
		},
	)
}

func (r *AgentRunReconciler) enforceWallclock(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	pod *corev1.Pod,
	limit *time.Duration,
) (ctrl.Result, error) {
	if limit == nil {
		return ctrl.Result{}, nil
	}

	if pod.Status.Phase != corev1.PodRunning {
		return ctrl.Result{}, nil
	}

	startedAt := status.ObservePod(pod).StartedAt
	if startedAt == nil {
		startedAt = run.Status.StartTime
	}
	if startedAt == nil {
		return ctrl.Result{}, nil
	}

	elapsed := r.now().Sub(startedAt.Time)
	if elapsed <= *limit {
		remaining := *limit - elapsed
		return ctrl.Result{RequeueAfter: remaining + time.Second}, nil
	}

	err := r.transitionRun(
		ctx,
		run,
		kontextv1alpha1.AgentRunPhaseBudgetExceeded,
		fmt.Sprintf("Wallclock budget exceeded after %s.", *limit),
		func(next *kontextv1alpha1.AgentRunStatus) {
			next.PodName = pod.Name
			recordedStart := *startedAt
			next.StartTime = &recordedStart
		},
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AgentRunReconciler) transitionRun(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	phase kontextv1alpha1.AgentRunPhase,
	message string,
	update func(*kontextv1alpha1.AgentRunStatus),
) error {
	return r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = phase
		next.Message = message
		if update != nil {
			update(next)
		}
		if phase.IsTerminal() {
			if next.CompletionTime == nil {
				next.CompletionTime = r.nowPtr()
			}
		} else {
			next.CompletionTime = nil
		}
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			conditions.ForAgentRunPhase(phase)...,
		)
	})
}

func (r *AgentRunReconciler) patchRunStatus(ctx context.Context, run *kontextv1alpha1.AgentRun, mutate func(*kontextv1alpha1.AgentRunStatus)) error {
	if err := patchStatus(ctx, r.Client, run, func() {
		mutate(&run.Status)
	}); err != nil {
		return err
	}
	return nil
}

func (r *AgentRunReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

func (r *AgentRunReconciler) nowPtr() *metav1.Time {
	now := metav1.NewTime(r.now())
	return &now
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kontextv1alpha1.AgentRun{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
