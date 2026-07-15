package conditions

import (
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/status"
)

const (
	Ready       = "Ready"
	Progressing = "Progressing"
	Complete    = "Complete"
	BudgetValid = "BudgetValid"
)

// Merge updates condition types in existing with new values, preserving transition times when unchanged.
func Merge(existing []metav1.Condition, updates ...metav1.Condition) []metav1.Condition {
	byType := map[string]metav1.Condition{}
	for _, condition := range existing {
		byType[condition.Type] = condition
	}
	now := metav1.Now()
	for _, update := range updates {
		current, ok := byType[update.Type]
		if ok && current.Status == update.Status && current.Reason == update.Reason && current.Message == update.Message {
			update.LastTransitionTime = current.LastTransitionTime
		} else {
			update.LastTransitionTime = now
		}
		byType[update.Type] = update
	}
	result := make([]metav1.Condition, 0, len(byType))
	for _, condition := range byType {
		result = append(result, condition)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Type < result[j].Type
	})
	return result
}

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

// ForAgentRunPhase returns merged run conditions for the given lifecycle phase.
func ForAgentRunPhase(phase kontextv1alpha1.AgentRunPhase, existing []metav1.Condition) []metav1.Condition {
	return Merge(existing, agentRunPhaseUpdates(phase)...)
}

func agentRunPhaseUpdates(phase kontextv1alpha1.AgentRunPhase) []metav1.Condition {
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

// BudgetConfigured returns a BudgetValid condition for wallclock parsing.
func BudgetConfigured(valid bool, message string) metav1.Condition {
	if valid {
		return metav1.Condition{
			Type:    BudgetValid,
			Status:  metav1.ConditionTrue,
			Reason:  "Configured",
			Message: "Wallclock budget is valid.",
		}
	}
	return metav1.Condition{
		Type:    BudgetValid,
		Status:  metav1.ConditionFalse,
		Reason:  "InvalidWallclock",
		Message: message,
	}
}
