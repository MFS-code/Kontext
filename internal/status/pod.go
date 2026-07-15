package status

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
)

// PodObservation summarizes a Pod for AgentRun reconciliation.
type PodObservation struct {
	Phase    kontextv1alpha1.AgentRunPhase
	Message  string
	Result   string
	Usage    *kontextv1alpha1.UsageStatus
	ExitCode *int32
}

// WallclockParseResult captures a parsed wallclock budget and any validation warning.
type WallclockParseResult struct {
	Duration time.Duration
	Valid    bool
	Warning  string
}

// ObservePod maps Pod status to AgentRun phase information.
func ObservePod(pod *corev1.Pod) PodObservation {
	if pod.Status.Phase == corev1.PodPending {
		return PodObservation{
			Phase:   kontextv1alpha1.AgentRunPhasePending,
			Message: "Agent run pod is waiting to start.",
		}
	}

	if len(pod.Status.ContainerStatuses) > 0 {
		state := pod.Status.ContainerStatuses[0].State
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
			return PodObservation{
				Phase:   kontextv1alpha1.AgentRunPhasePending,
				Message: fmt.Sprintf("Waiting: %s", state.Waiting.Message),
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

func observationFromTermination(terminated *corev1.ContainerStateTerminated) PodObservation {
	payload := ParseTerminationMessage(terminated.Message)
	usage := &kontextv1alpha1.UsageStatus{
		Tokens:  payload.TokensUsed,
		Dollars: payload.DollarsUsed,
	}
	exitCode := terminated.ExitCode

	if terminated.ExitCode == 0 {
		return PodObservation{
			Phase:    kontextv1alpha1.AgentRunPhaseSucceeded,
			Message:  "Agent run completed successfully.",
			Result:   payload.Result,
			Usage:    usage,
			ExitCode: &exitCode,
		}
	}

	message := fmt.Sprintf("Agent run exited with code %d.", terminated.ExitCode)
	if payload.Error != "" {
		message = fmt.Sprintf("%s %s", message, payload.Error)
	}

	return PodObservation{
		Phase:    kontextv1alpha1.AgentRunPhaseFailed,
		Message:  message,
		Result:   payload.Result,
		Usage:    usage,
		ExitCode: &exitCode,
	}
}

// ParseWallclock parses duration strings like 5m, 30s, 1h.
func ParseWallclock(value string, defaultSeconds int) time.Duration {
	return ParseWallclockDetailed(value, defaultSeconds).Duration
}

// ParseWallclockDetailed parses a wallclock budget and reports validation warnings.
func ParseWallclockDetailed(value string, defaultSeconds int) WallclockParseResult {
	defaultDuration := time.Duration(defaultSeconds) * time.Second
	value = strings.TrimSpace(value)
	if value == "" {
		return WallclockParseResult{Duration: defaultDuration, Valid: true}
	}

	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return WallclockParseResult{
			Duration: defaultDuration,
			Valid:    false,
			Warning:  fmt.Sprintf("Invalid wallclock budget %q; using default %s.", value, defaultDuration),
		}
	}
	return WallclockParseResult{Duration: duration, Valid: true}
}
