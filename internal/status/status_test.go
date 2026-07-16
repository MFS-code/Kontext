package status_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/status"
)

func TestParseTerminationMessageJSON(t *testing.T) {
	payload, err := status.ParseTerminationMessage(`{"result":"done","tokensUsed":42,"dollarsUsed":1.5}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload.Result != "done" {
		t.Fatalf("expected result done, got %q", payload.Result)
	}
	if payload.TokensUsed != 42 {
		t.Fatalf("expected tokens 42, got %d", payload.TokensUsed)
	}
	if payload.DollarsUsed != 1.5 {
		t.Fatalf("expected dollars 1.5, got %f", payload.DollarsUsed)
	}
}

func TestParseTerminationMessagePlainText(t *testing.T) {
	payload, err := status.ParseTerminationMessage("plain answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload.Result != "plain answer" {
		t.Fatalf("expected plain answer, got %q", payload.Result)
	}
}

func TestParseTerminationMessageMalformedJSON(t *testing.T) {
	raw := `{"result":"partial",`
	payload, err := status.ParseTerminationMessage(raw)
	if err == nil {
		t.Fatalf("expected error for malformed JSON payload")
	}
	if payload.Result != raw {
		t.Fatalf("expected raw message preserved as result, got %q", payload.Result)
	}
}

func TestObservePodMalformedTerminationOnSuccessFails(t *testing.T) {
	exitCode := int32(0)
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: exitCode,
							Message:  `{"result":"partial",`,
						},
					},
				},
			},
		},
	}
	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected Failed for malformed payload on exit 0, got %s", observation.Phase)
	}
	if observation.Result != "" {
		t.Fatalf("expected empty result for malformed payload, got %q", observation.Result)
	}
}

func TestObservePodRunning(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("expected Running, got %s", observation.Phase)
	}
}

func TestObservePodSucceeded(t *testing.T) {
	exitCode := int32(0)
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: exitCode,
							Message:  `{"result":"ok","tokensUsed":7}`,
						},
					},
				},
			},
		},
	}
	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("expected Succeeded, got %s", observation.Phase)
	}
	if observation.Result != "ok" {
		t.Fatalf("expected result ok, got %q", observation.Result)
	}
}

func TestParseWallclock(t *testing.T) {
	if got := status.ParseWallclock("5m", 30); got != 5*60*1e9 {
		t.Fatalf("expected 5m, got %v", got)
	}
	if got := status.ParseWallclock("", 30); got != 30*1e9 {
		t.Fatalf("expected default 30s, got %v", got)
	}
}

func TestParseWallclockDetailedInvalid(t *testing.T) {
	result := status.ParseWallclockDetailed("not-a-duration", 30)
	if result.Valid {
		t.Fatalf("expected invalid wallclock")
	}
	if result.Duration != 30*1e9 {
		t.Fatalf("expected default duration, got %v", result.Duration)
	}
	if result.Warning == "" {
		t.Fatalf("expected warning message")
	}
}

func TestObservePodWaitingUsesReason(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
					},
				},
			},
		},
	}
	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhasePending {
		t.Fatalf("expected Pending, got %s", observation.Phase)
	}
	if observation.Message != "Waiting: ContainerCreating" {
		t.Fatalf("expected reason in message, got %q", observation.Message)
	}
}

func TestObservePodWaitingIncludesMessage(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image \"missing:latest\"",
						},
					},
				},
			},
		},
	}
	observation := status.ObservePod(pod)
	if observation.Message != "Waiting: ImagePullBackOff (Back-off pulling image \"missing:latest\")" {
		t.Fatalf("unexpected message %q", observation.Message)
	}
}

func TestObservePodPending(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "run-demo"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhasePending {
		t.Fatalf("expected Pending, got %s", observation.Phase)
	}
}
