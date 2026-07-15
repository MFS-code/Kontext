package status

import kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"

// IsTerminalPhase reports whether an AgentRun phase is finished.
func IsTerminalPhase(phase kontextv1alpha1.AgentRunPhase) bool {
	switch phase {
	case kontextv1alpha1.AgentRunPhaseSucceeded,
		kontextv1alpha1.AgentRunPhaseFailed,
		kontextv1alpha1.AgentRunPhaseBudgetExceeded:
		return true
	default:
		return false
	}
}
