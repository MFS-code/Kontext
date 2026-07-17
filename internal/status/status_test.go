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
	payload, err := status.ParseTerminationMessage(`{"result":"done","tokensUsed":42,"dollarsUsed":1.5}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload.Result != "done" {
		t.Fatalf("expected result done, got %q", payload.Result)
	}
	if payload.Usage == nil || payload.Usage.TotalTokens == nil || *payload.Usage.TotalTokens != 42 {
		t.Fatalf("expected tokens 42, got %#v", payload.Usage)
	}
	if payload.Usage.Dollars == nil || *payload.Usage.Dollars != 1.5 {
		t.Fatalf("expected dollars 1.5, got %#v", payload.Usage)
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

func TestObservePodMalformedTerminationOnFailureNotesParseError(t *testing.T) {
	exitCode := int32(1)
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
		t.Fatalf("expected Failed, got %s", observation.Phase)
	}
	if !strings.Contains(observation.Message, "exited with code 1") {
		t.Fatalf("expected exit code in message, got %q", observation.Message)
	}
	if !strings.Contains(observation.Message, "malformed termination payload") {
		t.Fatalf("expected malformed payload note in message, got %q", observation.Message)
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

func TestObservePodPreservesVersionedStructuredOutputAndUsage(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message: `{
							"apiVersion":"kontext.dev/result/v1alpha1",
							"outcome":"Succeeded",
							"output":{"mediaType":"application/json","value":{"answer":42}},
							"usage":{"inputTokens":0,"outputTokens":7}
						}`,
					},
				},
			}},
		},
	}

	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("expected Succeeded, got %s", observation.Phase)
	}
	if observation.Output == nil || observation.Output.MediaType != "application/json" {
		t.Fatalf("unexpected structured output %#v", observation.Output)
	}
	if string(observation.Output.Value.Raw) != `{"answer":42}` {
		t.Fatalf("unexpected output value %s", observation.Output.Value.Raw)
	}
	if observation.Result != `{"answer":42}` {
		t.Fatalf("unexpected legacy result projection %q", observation.Result)
	}
	if observation.Usage == nil || observation.Usage.InputTokens == nil || *observation.Usage.InputTokens != 0 {
		t.Fatalf("expected measured zero input tokens, got %#v", observation.Usage)
	}
	if observation.Usage.OutputTokens == nil || *observation.Usage.OutputTokens != 7 {
		t.Fatalf("expected output token usage, got %#v", observation.Usage)
	}
	if observation.Usage.Tokens != nil || observation.Usage.Dollars != nil {
		t.Fatalf("missing metrics must remain absent, got %#v", observation.Usage)
	}
}

func TestObservePodFailedEnvelopeOverridesZeroExit(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Failed","error":{"message":"provider unavailable"}}`,
					},
				},
			}},
		},
	}

	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected Failed, got %s", observation.Phase)
	}
	if !strings.Contains(observation.Message, "provider unavailable") {
		t.Fatalf("expected reported error, got %q", observation.Message)
	}
}

func TestObservePodLegacyErrorDoesNotOverrideZeroExit(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  `{"result":"ok","error":"informational warning"}`,
					},
				},
			}},
		},
	}

	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("expected legacy exit 0 to remain Succeeded, got %s", observation.Phase)
	}
	if observation.Result != "ok" {
		t.Fatalf("expected legacy result, got %q", observation.Result)
	}
}

func TestObservePodLegacyPayloadWithoutMetricsLeavesUsageAbsent(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  `{"result":"ok"}`,
					},
				},
			}},
		},
	}
	if observation := status.ObservePod(pod); observation.Usage != nil {
		t.Fatalf("expected absent usage, got %#v", observation.Usage)
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
	if observation.Usage == nil || observation.Usage.Tokens == nil || *observation.Usage.Tokens != 9 ||
		observation.Usage.Dollars == nil || *observation.Usage.Dollars != 0.25 {
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
	payload, err := status.ParseTerminationMessage("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload.Result != "" || payload.Usage != nil {
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
