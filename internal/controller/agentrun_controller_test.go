package controller_test

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/conditions"
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

	podName := podbuilder.PodNameForRun(run.Name)
	pod := &corev1.Pod{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: run.Namespace}, pod); err != nil {
		t.Fatalf("expected pod %s: %v", podName, err)
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

func TestAgentRunReconcilerSurfacesInvalidWallclock(t *testing.T) {
	ctx := context.Background()
	podName := podbuilder.PodNameForRun("invalid-wallclock-run")
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "invalid-wallclock-run",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "hello",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
			Budget:   &kontextv1alpha1.BudgetSpec{Wallclock: "not-a-duration"},
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

	reconcileAgentRun(ctx, t, types.NamespacedName{Name: run.Name, Namespace: run.Namespace})

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &updated); err != nil {
		t.Fatalf("get run: %v", err)
	}
	found := false
	for _, condition := range updated.Status.Conditions {
		if condition.Type == conditions.BudgetValid && condition.Status == metav1.ConditionFalse {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected BudgetValid=False condition, got %#v", updated.Status.Conditions)
	}
	if updated.Status.Phase == kontextv1alpha1.AgentRunPhaseBudgetExceeded {
		t.Fatalf("invalid wallclock must not enforce a default budget")
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

func updateAgentRunStatus(ctx context.Context, run *kontextv1alpha1.AgentRun, next kontextv1alpha1.AgentRunStatus) error {
	run.Status = next
	return k8sClient.Status().Update(ctx, run)
}
