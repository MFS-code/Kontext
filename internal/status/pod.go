package status

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

// PodObservation summarizes a Pod for AgentRun reconciliation.
type PodObservation struct {
	Phase     kontextv1alpha1.AgentRunPhase
	Message   string
	Result    string
	Output    *kontextv1alpha1.OutputStatus
	Usage     *kontextv1alpha1.UsageStatus
	StartedAt *metav1.Time
}

// ObservePod maps Pod status to AgentRun phase information.
func ObservePod(pod *corev1.Pod) PodObservation {
	if pod.Status.Phase == corev1.PodPending {
		return PodObservation{
			Phase:   kontextv1alpha1.AgentRunPhasePending,
			Message: "Agent run pod is waiting to start.",
		}
	}

	if containerStatus := runtimeContainerStatus(pod); containerStatus != nil {
		state := containerStatus.State
		if state.Running != nil {
			startedAt := state.Running.StartedAt
			return PodObservation{
				Phase:     kontextv1alpha1.AgentRunPhaseRunning,
				Message:   "Agent run pod is streaming thoughts.",
				StartedAt: &startedAt,
			}
		}
		if state.Terminated != nil {
			return observationFromTermination(state.Terminated)
		}
		if state.Waiting != nil && state.Waiting.Reason != "" {
			message := fmt.Sprintf("Waiting: %s", state.Waiting.Reason)
			if state.Waiting.Message != "" {
				message = fmt.Sprintf("%s (%s)", message, state.Waiting.Message)
			}
			return PodObservation{
				Phase:   kontextv1alpha1.AgentRunPhasePending,
				Message: message,
			}
		}
	}

	switch pod.Status.Phase {
	case corev1.PodRunning:
		return PodObservation{
			Phase:   kontextv1alpha1.AgentRunPhaseRunning,
			Message: "Agent run pod is streaming thoughts.",
		}
	case corev1.PodSucceeded:
		return PodObservation{
			Phase:   kontextv1alpha1.AgentRunPhaseSucceeded,
			Message: "Agent run pod completed.",
		}
	case corev1.PodFailed:
		return PodObservation{
			Phase:   kontextv1alpha1.AgentRunPhaseFailed,
			Message: "Agent run pod failed.",
		}
	default:
		return PodObservation{
			Phase:   kontextv1alpha1.AgentRunPhasePending,
			Message: "Agent run pod status is not available yet.",
		}
	}
}

func runtimeContainerStatus(pod *corev1.Pod) *corev1.ContainerStatus {
	for index := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[index].Name == podbuilder.RuntimeContainerName {
			return &pod.Status.ContainerStatuses[index]
		}
	}
	return nil
}

func observationFromTermination(terminated *corev1.ContainerStateTerminated) PodObservation {
	message := strings.TrimSpace(terminated.Message)
	envelope, _, parseErr := resultv1alpha1.Parse(message)
	if parseErr != nil {
		parseErr = fmt.Errorf("parse termination payload: %w", parseErr)
	}
	output := outputStatus(envelope)
	usage := usageStatus(envelope)
	legacyResult := resultv1alpha1.PlainText(envelope.Output)

	if terminated.ExitCode == 0 {
		if parseErr != nil {
			// The container reported success but emitted a payload that looked
			// like JSON and failed to decode. Do not silently accept it as the
			// result: mark the run failed so the malformed output is visible.
			return PodObservation{
				Phase:   kontextv1alpha1.AgentRunPhaseFailed,
				Message: fmt.Sprintf("Agent run exited 0 but the termination payload was malformed: %v", parseErr),
			}
		}
		if envelope.Outcome == resultv1alpha1.OutcomeFailed {
			message := "Agent runtime reported a failed outcome."
			if envelope.Error != nil {
				message = fmt.Sprintf("%s %s", message, envelope.Error.Message)
			}
			return PodObservation{
				Phase:   kontextv1alpha1.AgentRunPhaseFailed,
				Message: message,
				Result:  legacyResult,
				Output:  output,
				Usage:   usage,
			}
		}
		return PodObservation{
			Phase:   kontextv1alpha1.AgentRunPhaseSucceeded,
			Message: "Agent run completed successfully.",
			Result:  legacyResult,
			Output:  output,
			Usage:   usage,
		}
	}

	message = fmt.Sprintf("Agent run exited with code %d.", terminated.ExitCode)
	if envelope.Error != nil {
		message = fmt.Sprintf("%s %s", message, envelope.Error.Message)
	}
	if parseErr != nil {
		message = fmt.Sprintf("%s (malformed termination payload: %v)", message, parseErr)
		legacyResult = strings.TrimSpace(terminated.Message)
	}

	return PodObservation{
		Phase:   kontextv1alpha1.AgentRunPhaseFailed,
		Message: message,
		Result:  legacyResult,
		Output:  output,
		Usage:   usage,
	}
}

func outputStatus(envelope resultv1alpha1.Envelope) *kontextv1alpha1.OutputStatus {
	if envelope.Output == nil {
		return nil
	}
	return &kontextv1alpha1.OutputStatus{
		MediaType: envelope.Output.MediaType,
		Value: runtime.RawExtension{
			Raw: append([]byte(nil), envelope.Output.Value...),
		},
	}
}

func usageStatus(envelope resultv1alpha1.Envelope) *kontextv1alpha1.UsageStatus {
	if envelope.Usage == nil {
		return nil
	}
	return &kontextv1alpha1.UsageStatus{
		Tokens:       envelope.Usage.TotalTokens,
		InputTokens:  envelope.Usage.InputTokens,
		OutputTokens: envelope.Usage.OutputTokens,
		Dollars:      envelope.Usage.Dollars,
	}
}
