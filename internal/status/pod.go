package status

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/podbuilder"
	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
)

// PodObservation summarizes a Pod for AgentRun reconciliation.
type PodObservation struct {
	Phase    kontextv1alpha1.AgentRunPhase
	Message  string
	Result   string
	Output   *kontextv1alpha1.OutputStatus
	Usage    *kontextv1alpha1.UsageStatus
	ExitCode *int32
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
			return PodObservation{
				Phase:   kontextv1alpha1.AgentRunPhaseRunning,
				Message: "Agent run pod is streaming thoughts.",
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
	parsed, parseErr := resultv1alpha1.Parse(message)
	if parseErr != nil {
		parseErr = fmt.Errorf("parse termination payload: %w", parseErr)
	}
	output := outputStatus(parsed)
	usage := usageStatus(parsed)
	legacyResult := resultv1alpha1.ProjectLegacyResult(parsed.Output)
	exitCode := terminated.ExitCode

	if terminated.ExitCode == 0 {
		if parseErr != nil {
			// The container reported success but emitted a payload that looked
			// like JSON and failed to decode. Do not silently accept it as the
			// result: mark the run failed so the malformed output is visible.
			return PodObservation{
				Phase:    kontextv1alpha1.AgentRunPhaseFailed,
				Message:  fmt.Sprintf("Agent run exited 0 but the termination payload was malformed: %v", parseErr),
				ExitCode: &exitCode,
			}
		}
		if parsed.Outcome == resultv1alpha1.OutcomeFailed {
			message := "Agent runtime reported a failed outcome."
			if parsed.Error != nil {
				message = fmt.Sprintf("%s %s", message, parsed.Error.Message)
			}
			return PodObservation{
				Phase:    kontextv1alpha1.AgentRunPhaseFailed,
				Message:  message,
				Result:   legacyResult,
				Output:   output,
				Usage:    usage,
				ExitCode: &exitCode,
			}
		}
		return PodObservation{
			Phase:    kontextv1alpha1.AgentRunPhaseSucceeded,
			Message:  "Agent run completed successfully.",
			Result:   legacyResult,
			Output:   output,
			Usage:    usage,
			ExitCode: &exitCode,
		}
	}

	message = fmt.Sprintf("Agent run exited with code %d.", terminated.ExitCode)
	if parsed.Error != nil {
		message = fmt.Sprintf("%s %s", message, parsed.Error.Message)
	}
	if parseErr != nil {
		message = fmt.Sprintf("%s (malformed termination payload: %v)", message, parseErr)
		legacyResult = strings.TrimSpace(terminated.Message)
	}

	return PodObservation{
		Phase:    kontextv1alpha1.AgentRunPhaseFailed,
		Message:  message,
		Result:   legacyResult,
		Output:   output,
		Usage:    usage,
		ExitCode: &exitCode,
	}
}

func outputStatus(parsed resultv1alpha1.ParsedResult) *kontextv1alpha1.OutputStatus {
	if parsed.Output == nil {
		return nil
	}
	return &kontextv1alpha1.OutputStatus{
		MediaType: parsed.Output.MediaType,
		Value: runtime.RawExtension{
			Raw: append([]byte(nil), parsed.Output.Value...),
		},
	}
}

func usageStatus(parsed resultv1alpha1.ParsedResult) *kontextv1alpha1.UsageStatus {
	if parsed.Usage == nil {
		return nil
	}
	return &kontextv1alpha1.UsageStatus{
		Tokens:       parsed.Usage.TotalTokens,
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		Dollars:      parsed.Usage.Dollars,
	}
}

// ParseWallclock parses a configured positive duration.
func ParseWallclock(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("wallclock budget is empty")
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid wallclock budget %q: %w", value, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid wallclock budget %q: duration must be positive", value)
	}
	return duration, nil
}
