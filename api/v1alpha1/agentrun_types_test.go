package v1alpha1_test

import (
	"testing"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestAgentRunPhaseIsTerminal(t *testing.T) {
	cases := map[kontextv1alpha1.AgentRunPhase]bool{
		kontextv1alpha1.AgentRunPhaseSucceeded:      true,
		kontextv1alpha1.AgentRunPhaseFailed:         true,
		kontextv1alpha1.AgentRunPhaseBudgetExceeded: true,
		kontextv1alpha1.AgentRunPhasePending:        false,
		kontextv1alpha1.AgentRunPhaseRunning:        false,
		kontextv1alpha1.AgentRunPhase("Bogus"):      false,
	}
	for phase, want := range cases {
		if got := phase.IsTerminal(); got != want {
			t.Fatalf("%s.IsTerminal() = %v, want %v", phase, got, want)
		}
	}
}
