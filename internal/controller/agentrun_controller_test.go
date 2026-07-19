package controller_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/podbuilder"
)

func TestAgentRunReconcilerCreatesPod(t *testing.T) {
	ctx := context.Background()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "create-pod-run",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	reconcileAgentRun(ctx, t, types.NamespacedName{Name: run.Name, Namespace: run.Namespace})

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhasePending {
		t.Fatalf("expected Pending, got %s", updated.Status.Phase)
	}
	for _, condition := range updated.Status.Conditions {
		if condition.ObservedGeneration != updated.Generation {
			t.Fatalf(
				"condition %s observed generation %d, want %d",
				condition.Type,
				condition.ObservedGeneration,
				updated.Generation,
			)
		}
	}

	podName := podbuilder.PodNameForRun(run.Name)
	pod := &corev1.Pod{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: run.Namespace}, pod); err != nil {
		t.Fatalf("expected pod %s: %v", podName, err)
	}
}

func TestAgentRunSpecIsImmutable(t *testing.T) {
	ctx := context.Background()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "immutable-run", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "original goal",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	run.Spec.Goal = "changed goal"
	if err := k8sClient.Update(ctx, run); err == nil {
		t.Fatal("expected immutable AgentRun spec update to be rejected")
	}
}

func TestAgentRunRejectsUnsafeRuntimeSecurityContext(t *testing.T) {
	trueValue := true
	tests := []struct {
		name            string
		securityContext *kontextv1alpha1.RuntimeSecurityContext
	}{
		{
			name: "privilege escalation",
			securityContext: &kontextv1alpha1.RuntimeSecurityContext{
				AllowPrivilegeEscalation: &trueValue,
			},
		},
		{
			name: "localhost seccomp without profile",
			securityContext: &kontextv1alpha1.RuntimeSecurityContext{
				SeccompProfile: &kontextv1alpha1.RuntimeSeccompProfile{
					Type: "Localhost",
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := &kontextv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unsafe-" + strings.ReplaceAll(test.name, " ", "-"),
					Namespace: "default",
				},
				Spec: kontextv1alpha1.AgentRunSpec{
					Goal:     "goal",
					Provider: "echo",
					Model:    "echo-model",
					Runtime: kontextv1alpha1.RuntimeSpec{
						Image:           "runtime:dev",
						SecurityContext: test.securityContext,
					},
				},
			}
			if err := k8sClient.Create(context.Background(), run); err == nil {
				t.Fatal("expected unsafe security context to be rejected")
			}
		})
	}
}

func TestAgentRunReconcilerCreatesReporterWrappedPod(t *testing.T) {
	ctx := context.Background()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stdout-capture-run",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:   "busybox:1.36.1",
				Command: []string{"sh", "-c"},
				Args:    []string{"echo final answer"},
				Result: &kontextv1alpha1.RuntimeResultSpec{
					Source: kontextv1alpha1.ResultSourceStdout,
					Format: kontextv1alpha1.ResultFormatLastLine,
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	reconcileAgentRun(ctx, t, types.NamespacedName{Name: run.Name, Namespace: run.Namespace})

	var pod corev1.Pod
	podName := podbuilder.PodNameForRun(run.Name)
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: run.Namespace}, &pod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if len(pod.Spec.InitContainers) != 1 ||
		pod.Spec.InitContainers[0].Image != "kontext-reporter:dev" {
		t.Fatalf("expected trusted reporter init container, got %#v", pod.Spec.InitContainers)
	}
	if got := pod.Spec.Containers[0].Command[0]; got != podbuilder.ReporterBinaryPath {
		t.Fatalf("expected reporter command, got %q", got)
	}
	if pod.Spec.Containers[0].Image != "busybox:1.36.1" {
		t.Fatalf("workload image changed: %q", pod.Spec.Containers[0].Image)
	}
}

func TestAgentRunReconcilerFailsWhenReporterImageIsUnconfigured(t *testing.T) {
	ctx := context.Background()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "missing-reporter-image",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:   "busybox:1.36.1",
				Command: []string{"echo", "hello"},
				Result: &kontextv1alpha1.RuntimeResultSpec{
					Source: kontextv1alpha1.ResultSourceStdout,
					Format: kontextv1alpha1.ResultFormatLastLine,
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	reconciler := newAgentRunReconciler()
	reconciler.ReporterImage = ""
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace},
	}); err != nil {
		t.Fatalf("reconcile run: %v", err)
	}

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected Failed, got %s", updated.Status.Phase)
	}
	if updated.Status.Message != "Agent run configuration is invalid: reporter image is not configured." ||
		updated.Status.CompletionTime == nil {
		t.Fatalf("expected actionable terminal status, got %#v", updated.Status)
	}
}

func TestAgentRunRejectsStdoutCaptureWithoutCommand(t *testing.T) {
	ctx := context.Background()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "invalid-stdout-capture",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image: "busybox:1.36.1",
				Result: &kontextv1alpha1.RuntimeResultSpec{
					Source: kontextv1alpha1.ResultSourceStdout,
					Format: kontextv1alpha1.ResultFormatLastLine,
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, run); err == nil {
		t.Fatalf("expected API validation to require runtime.command")
	}
}

func TestAgentRunRejectsStdoutCaptureWithEmptyExecutable(t *testing.T) {
	ctx := context.Background()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "empty-stdout-command",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:   "busybox:1.36.1",
				Command: []string{""},
				Result: &kontextv1alpha1.RuntimeResultSpec{
					Source: kontextv1alpha1.ResultSourceStdout,
					Format: kontextv1alpha1.ResultFormatLastLine,
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, run); err == nil {
		t.Fatalf("expected API validation to reject an empty command executable")
	}
}

func TestAgentRunReconcilerFailsWhenPodLost(t *testing.T) {
	ctx := context.Background()
	podName := podbuilder.PodNameForRun("lost-pod-run")
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lost-pod-run",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase:   kontextv1alpha1.AgentRunPhaseRunning,
		PodName: podName,
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	reconcileAgentRun(ctx, t, types.NamespacedName{Name: run.Name, Namespace: run.Namespace})

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected Failed, got %s", updated.Status.Phase)
	}
	if updated.Status.CompletionTime == nil {
		t.Fatalf("expected completion time")
	}
}

func TestAgentRunReconcilerObservesSucceededPod(t *testing.T) {
	ctx := context.Background()
	podName := podbuilder.PodNameForRun("success-run")
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "success-run",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase:   kontextv1alpha1.AgentRunPhasePending,
		PodName: podName,
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	pod := podbuilder.BuildPod(run)
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
	var createdPod corev1.Pod
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &createdPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	createdPod.Status.Phase = corev1.PodRunning
	createdPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "runtime",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0,
				Message:  `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","output":{"mediaType":"application/json","value":["done",42]},"usage":{"totalTokens":3}}`,
			},
		},
	}}
	if err := k8sClient.Status().Update(ctx, &createdPod); err != nil {
		t.Fatalf("update pod status: %v", err)
	}

	reconcileAgentRun(ctx, t, types.NamespacedName{Name: run.Name, Namespace: run.Namespace})

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("expected Succeeded, got %s", updated.Status.Phase)
	}
	if updated.Status.Result != `["done",42]` {
		t.Fatalf("unexpected legacy result projection %q", updated.Status.Result)
	}
	if updated.Status.Output == nil || updated.Status.Output.MediaType != "application/json" {
		t.Fatalf("expected structured output, got %#v", updated.Status.Output)
	}
	if string(updated.Status.Output.Value.Raw) != `["done",42]` {
		t.Fatalf("unexpected structured output value %s", updated.Status.Output.Value.Raw)
	}
	if updated.Status.Usage == nil || updated.Status.Usage.Tokens == nil || *updated.Status.Usage.Tokens != 3 {
		t.Fatalf("expected total token usage, got %#v", updated.Status.Usage)
	}
}

func TestAgentRunReconcilerObservesRunningPodWithinWallclockBudget(t *testing.T) {
	ctx := context.Background()
	podName := podbuilder.PodNameForRun("active-wallclock-run")
	started := metav1.NewTime(time.Now().Truncate(time.Second).Add(-time.Second))
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "active-wallclock-run",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
			Budget:   &kontextv1alpha1.BudgetSpec{Wallclock: "1m"},
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase:   kontextv1alpha1.AgentRunPhasePending,
		PodName: podName,
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	pod := podbuilder.BuildPod(run)
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
	var createdPod corev1.Pod
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &createdPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	createdPod.Status.Phase = corev1.PodRunning
	createdPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "runtime",
		State: corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{StartedAt: started},
		},
	}}
	if err := k8sClient.Status().Update(ctx, &createdPod); err != nil {
		t.Fatalf("update pod status: %v", err)
	}

	reconcileAgentRun(ctx, t, types.NamespacedName{Name: run.Name, Namespace: run.Namespace})

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("expected Running, got %s", updated.Status.Phase)
	}
	if updated.Status.StartTime == nil {
		t.Fatalf("expected start time")
	}
	if !updated.Status.StartTime.Time.Equal(started.Time) {
		t.Fatalf("expected runtime container start %s, got %s", started, updated.Status.StartTime)
	}
}

func TestAgentRunReconcilerEnforcesWallclockBudget(t *testing.T) {
	ctx := context.Background()
	podName := podbuilder.PodNameForRun("wallclock-run")
	started := metav1.NewTime(time.Now().Truncate(time.Second).Add(-2 * time.Minute))
	recordedStarted := metav1.NewTime(time.Now())
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wallclock-run",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
			Budget:   &kontextv1alpha1.BudgetSpec{Wallclock: "1s"},
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase:     kontextv1alpha1.AgentRunPhaseRunning,
		PodName:   podName,
		StartTime: &recordedStarted,
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	pod := podbuilder.BuildPod(run)
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
	var createdPod corev1.Pod
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &createdPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	createdPod.Status.Phase = corev1.PodRunning
	createdPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "runtime",
		State: corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{StartedAt: started},
		},
	}}
	if err := k8sClient.Status().Update(ctx, &createdPod); err != nil {
		t.Fatalf("update pod status: %v", err)
	}

	reconcileAgentRun(ctx, t, types.NamespacedName{Name: run.Name, Namespace: run.Namespace})

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseBudgetExceeded {
		t.Fatalf("expected BudgetExceeded, got %s", updated.Status.Phase)
	}
	if updated.Status.StartTime == nil || !updated.Status.StartTime.Time.Equal(started.Time) {
		t.Fatalf("expected actual runtime start %s, got %v", started, updated.Status.StartTime)
	}
}

func TestAgentRunReconcilerKeepsWallclockBudgetExceededAuthoritative(t *testing.T) {
	ctx := context.Background()
	runName := "wallclock-cancellation-race"
	podName := podbuilder.PodNameForRun(runName)
	started := metav1.NewTime(time.Now().Truncate(time.Second).Add(-2 * time.Minute))
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "sleep",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
			Budget:   &kontextv1alpha1.BudgetSpec{Wallclock: "2s"},
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase:     kontextv1alpha1.AgentRunPhaseRunning,
		PodName:   podName,
		StartTime: &started,
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	pod := podbuilder.BuildPod(run)
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
	var createdPod corev1.Pod
	podKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
	if err := k8sClient.Get(ctx, podKey, &createdPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	createdPod.Status.Phase = corev1.PodRunning
	createdPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: podbuilder.RuntimeContainerName,
		State: corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{StartedAt: started},
		},
	}}
	if err := k8sClient.Status().Update(ctx, &createdPod); err != nil {
		t.Fatalf("update pod status: %v", err)
	}

	deleteErr := errors.New("injected pod deletion failure")
	reconciler := newAgentRunReconciler()
	reconciler.Client = &deleteErrorClient{Client: k8sClient, err: deleteErr}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace},
	})
	if !errors.Is(err, deleteErr) {
		t.Fatalf("expected injected deletion failure, got %v", err)
	}

	var budgetExceeded kontextv1alpha1.AgentRun
	runKey := types.NamespacedName{Name: run.Name, Namespace: run.Namespace}
	if err := k8sClient.Get(ctx, runKey, &budgetExceeded); err != nil {
		t.Fatalf("get budget-exceeded run: %v", err)
	}
	if budgetExceeded.Status.Phase != kontextv1alpha1.AgentRunPhaseBudgetExceeded {
		t.Fatalf("expected BudgetExceeded before pod deletion, got %s", budgetExceeded.Status.Phase)
	}

	if err := k8sClient.Get(ctx, podKey, &createdPod); err != nil {
		t.Fatalf("get pod after failed deletion: %v", err)
	}
	createdPod.Status.Phase = corev1.PodFailed
	createdPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: podbuilder.RuntimeContainerName,
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1,
				Message:  `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Failed","error":{"code":"cancelled","message":"runtime execution was cancelled"}}`,
			},
		},
	}}
	if err := k8sClient.Status().Update(ctx, &createdPod); err != nil {
		t.Fatalf("record cancelled runtime termination: %v", err)
	}

	reconcileAgentRun(ctx, t, runKey)

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, runKey, &updated); err != nil {
		t.Fatalf("get reconciled run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseBudgetExceeded {
		t.Fatalf("expected BudgetExceeded to remain authoritative, got %s", updated.Status.Phase)
	}
	if updated.Status.Message != "Wallclock budget exceeded after 2s." {
		t.Fatalf("unexpected budget status message %q", updated.Status.Message)
	}
	if err := k8sClient.Get(ctx, podKey, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected terminated pod to be deleted, got %v", err)
	}

	reconcileAgentRun(ctx, t, runKey)

	if err := k8sClient.Get(ctx, runKey, &updated); err != nil {
		t.Fatalf("get run after pod disappearance: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseBudgetExceeded {
		t.Fatalf("expected BudgetExceeded after pod disappearance, got %s", updated.Status.Phase)
	}
}

func TestAgentRunReconcilerPreservesRuntimeCancellationBeforeWallclockEnforcement(t *testing.T) {
	ctx := context.Background()
	runName := "runtime-cancellation"
	podName := podbuilder.PodNameForRun(runName)
	started := metav1.NewTime(time.Now().Truncate(time.Second).Add(-time.Minute))
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "sleep",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
			Budget:   &kontextv1alpha1.BudgetSpec{Wallclock: "2s"},
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase:     kontextv1alpha1.AgentRunPhaseRunning,
		PodName:   podName,
		StartTime: &started,
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	pod := podbuilder.BuildPod(run)
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
	var createdPod corev1.Pod
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &createdPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	createdPod.Status.Phase = corev1.PodFailed
	createdPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: podbuilder.RuntimeContainerName,
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1,
				Message:  `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Failed","error":{"code":"cancelled","message":"runtime execution was cancelled"}}`,
			},
		},
	}}
	if err := k8sClient.Status().Update(ctx, &createdPod); err != nil {
		t.Fatalf("update pod status: %v", err)
	}

	runKey := types.NamespacedName{Name: run.Name, Namespace: run.Namespace}
	reconcileAgentRun(ctx, t, runKey)

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, runKey, &updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected ordinary runtime cancellation to remain Failed, got %s", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.Message, "runtime execution was cancelled") {
		t.Fatalf("expected runtime cancellation diagnostics, got %q", updated.Status.Message)
	}
}

type deleteErrorClient struct {
	client.Client
	err error
}

func (c *deleteErrorClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return c.err
}

func updateAgentRunStatus(ctx context.Context, run *kontextv1alpha1.AgentRun, next kontextv1alpha1.AgentRunStatus) error {
	run.Status = next
	return k8sClient.Status().Update(ctx, run)
}
