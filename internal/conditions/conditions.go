package conditions

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/status"
)

const (
	Ready       = "Ready"
	Progressing = "Progressing"
	Complete    = "Complete"
)

// UnsupportedMode returns conditions for unimplemented Agent modes.
func UnsupportedMode(mode string) []metav1.Condition {
	return []metav1.Condition{{
		Type:    Ready,
		Status:  metav1.ConditionFalse,
		Reason:  "UnsupportedMode",
		Message: fmt.Sprintf("%s mode is not implemented yet.", mode),
	}, {
		Type:    Progressing,
		Status:  metav1.ConditionFalse,
		Reason:  "UnsupportedMode",
		Message: fmt.Sprintf("%s mode reconciliation is not available.", mode),
	}}
}

// InvalidMode returns conditions for unknown Agent modes.
func InvalidMode(mode string) []metav1.Condition {
	return []metav1.Condition{{
		Type:    Ready,
		Status:  metav1.ConditionFalse,
		Reason:  "InvalidMode",
		Message: fmt.Sprintf("Unknown agent mode %q.", mode),
	}}
}

// ForAgentRunPhase returns conditions for the given lifecycle phase.
func ForAgentRunPhase(phase kontextv1alpha1.AgentRunPhase) []metav1.Condition {
	switch {
	case status.IsTerminalPhase(phase):
		return []metav1.Condition{{
			Type:    Complete,
			Status:  metav1.ConditionTrue,
			Reason:  string(phase),
			Message: "Agent run finished.",
		}, {
			Type:    Progressing,
			Status:  metav1.ConditionFalse,
			Reason:  string(phase),
			Message: "Agent run is no longer active.",
		}}
	case phase == kontextv1alpha1.AgentRunPhaseRunning:
		return []metav1.Condition{{
			Type:    Complete,
			Status:  metav1.ConditionFalse,
			Reason:  "Running",
			Message: "Agent run is active.",
		}, {
			Type:    Progressing,
			Status:  metav1.ConditionTrue,
			Reason:  "Running",
			Message: "Agent run pod is executing.",
		}}
	default:
		return []metav1.Condition{{
			Type:    Complete,
			Status:  metav1.ConditionFalse,
			Reason:  string(phase),
			Message: "Agent run has not completed.",
		}, {
			Type:    Progressing,
			Status:  metav1.ConditionTrue,
			Reason:  string(phase),
			Message: "Agent run is starting.",
		}}
	}
}
