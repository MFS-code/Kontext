package status_test

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/status"
)

func TestParseTerminationMessageJSON(t *testing.T) {
	payload := status.ParseTerminationMessage(`{"result":"done","tokensUsed":42,"dollarsUsed":1.5}`)
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
	payload := status.ParseTerminationMessage("plain answer")
	if payload.Result != "plain answer" {
		t.Fatalf("expected plain answer, got %q", payload.Result)
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

func TestIsTerminalPhase(t *testing.T) {
	cases := map[kontextv1alpha1.AgentRunPhase]bool{
		kontextv1alpha1.AgentRunPhaseSucceeded:      true,
		kontextv1alpha1.AgentRunPhaseFailed:         true,
		kontextv1alpha1.AgentRunPhaseBudgetExceeded: true,
		kontextv1alpha1.AgentRunPhasePending:        false,
		kontextv1alpha1.AgentRunPhaseRunning:        false,
		kontextv1alpha1.AgentRunPhase("Bogus"):      false,
	}
	for phase, want := range cases {
		if got := status.IsTerminalPhase(phase); got != want {
			t.Fatalf("IsTerminalPhase(%s) = %v, want %v", phase, got, want)
		}
	}
}

func TestObservePodTerminatedFailure(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 2,
							Message:  `{"result":"partial","error":"boom","tokensUsed":9,"dollarsUsed":0.25}`,
						},
					},
				},
			},
		},
	}
	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected Failed, got %s", observation.Phase)
	}
	if observation.ExitCode == nil || *observation.ExitCode != 2 {
		t.Fatalf("expected exit code 2, got %v", observation.ExitCode)
	}
	if observation.Result != "partial" {
		t.Fatalf("expected result partial, got %q", observation.Result)
	}
	if !strings.Contains(observation.Message, "code 2") || !strings.Contains(observation.Message, "boom") {
		t.Fatalf("expected exit code and error in message, got %q", observation.Message)
	}
	if observation.Usage == nil || observation.Usage.Tokens != 9 || observation.Usage.Dollars != 0.25 {
		t.Fatalf("unexpected usage: %#v", observation.Usage)
	}
}

func TestObservePodTerminatedFailureWithoutError(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{ExitCode: 1},
					},
				},
			},
		},
	}
	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected Failed, got %s", observation.Phase)
	}
	if observation.Message != "Agent run exited with code 1." {
		t.Fatalf("unexpected message: %q", observation.Message)
	}
}

func TestObservePodRunningFallbackPhase(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if got := status.ObservePod(pod).Phase; got != kontextv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("expected Running, got %s", got)
	}
}

func TestObservePodSucceededFallbackPhase(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	if got := status.ObservePod(pod).Phase; got != kontextv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("expected Succeeded, got %s", got)
	}
}

func TestObservePodFailedFallbackPhase(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}}
	if got := status.ObservePod(pod).Phase; got != kontextv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected Failed, got %s", got)
	}
}

func TestObservePodUnknownPhaseDefaultsPending(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodUnknown}}
	if got := status.ObservePod(pod).Phase; got != kontextv1alpha1.AgentRunPhasePending {
		t.Fatalf("expected Pending, got %s", got)
	}
}

func TestParseTerminationMessageEmpty(t *testing.T) {
	payload := status.ParseTerminationMessage("   ")
	if payload.Result != "" || payload.TokensUsed != 0 {
		t.Fatalf("expected empty payload, got %#v", payload)
	}
}

func TestParseWallclockDetailedValidDuration(t *testing.T) {
	result := status.ParseWallclockDetailed("90s", 30)
	if !result.Valid {
		t.Fatalf("expected valid result")
	}
	if result.Duration != 90*time.Second {
		t.Fatalf("expected 90s, got %v", result.Duration)
	}
	if result.Warning != "" {
		t.Fatalf("expected no warning, got %q", result.Warning)
	}
}

func TestParseWallclockDetailedNonPositive(t *testing.T) {
	result := status.ParseWallclockDetailed("0s", 30)
	if result.Valid {
		t.Fatalf("expected non-positive duration to be invalid")
	}
	if result.Duration != 30*time.Second {
		t.Fatalf("expected default duration, got %v", result.Duration)
	}
}
