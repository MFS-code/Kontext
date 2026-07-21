package status_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/status"
)

func TestObservePodPlainTextTermination(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: podbuilder.RuntimeContainerName,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  "plain answer",
					},
				},
			}},
		},
	}

	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("expected Succeeded, got %s", observation.Phase)
	}
	if observation.Result != "plain answer" {
		t.Fatalf("expected plain answer, got %q", observation.Result)
	}
	if observation.Output == nil || observation.Output.MediaType != "text/plain" {
		t.Fatalf("expected text output, got %#v", observation.Output)
	}
}

func TestObservePodEmptyTerminationIsExplicitSuccess(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: podbuilder.RuntimeContainerName,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  " \t\n",
					},
				},
			}},
		},
	}

	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("expected Succeeded, got %s", observation.Phase)
	}
	if observation.Result != "" || observation.Output != nil || observation.Usage != nil {
		t.Fatalf("empty success invented status data: %#v", observation)
	}
}

func TestObservePodMalformedTerminationOnSuccessFails(t *testing.T) {
	exitCode := int32(0)
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: podbuilder.RuntimeContainerName,
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: exitCode,
							Message:  `{"partial":`,
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
	if !strings.Contains(observation.Message, "termination payload was malformed") {
		t.Fatalf("expected malformed payload message, got %q", observation.Message)
	}
}

func TestObservePodMalformedTerminationOnFailureNotesParseError(t *testing.T) {
	exitCode := int32(1)
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: podbuilder.RuntimeContainerName,
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
	startedAt := metav1.Now()
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: podbuilder.RuntimeContainerName,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{StartedAt: startedAt},
					},
				},
			},
		},
	}
	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("expected Running, got %s", observation.Phase)
	}
	if observation.StartedAt == nil || !observation.StartedAt.Equal(&startedAt) {
		t.Fatalf("expected runtime start %s, got %v", startedAt, observation.StartedAt)
	}
}

func TestObservePodSelectsRuntimeContainerByName(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "unrelated-sidecar",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
							Message:  `{"result":"wrong container"}`,
						},
					},
				},
				{
					Name:  podbuilder.RuntimeContainerName,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}

	observation := status.ObservePod(pod)
	if observation.Phase != kontextv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("expected runtime container to remain Running, got %s", observation.Phase)
	}
}

func TestObservePodSucceeded(t *testing.T) {
	exitCode := int32(0)
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: podbuilder.RuntimeContainerName,
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
				Name: podbuilder.RuntimeContainerName,
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
				Name: podbuilder.RuntimeContainerName,
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
				Name: podbuilder.RuntimeContainerName,
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
				Name: podbuilder.RuntimeContainerName,
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

func TestObservePodWaitingUsesReason(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: podbuilder.RuntimeContainerName,
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
					Name: podbuilder.RuntimeContainerName,
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

func TestObservePodTerminatedFailure(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: podbuilder.RuntimeContainerName,
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
					Name: podbuilder.RuntimeContainerName,
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
