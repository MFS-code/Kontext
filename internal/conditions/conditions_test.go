package conditions_test

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/conditions"
)

func TestMergePreservesTransitionTimeWhenUnchanged(t *testing.T) {
	stable := metav1.Now()
	existing := []metav1.Condition{{
		Type:               conditions.Ready,
		Status:             metav1.ConditionTrue,
		Reason:             "RunActive",
		Message:            "Service run echo-service-1 is active.",
		LastTransitionTime: stable,
	}}

	merged := conditions.Merge(existing, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionTrue,
		Reason:  "RunActive",
		Message: "Service run echo-service-1 is active.",
	})
	if len(merged) != 1 {
		t.Fatalf("expected one condition, got %d", len(merged))
	}
	if !merged[0].LastTransitionTime.Equal(&stable) {
		t.Fatalf("expected stable transition time, got %v", merged[0].LastTransitionTime)
	}
}

func TestMergeUpdatesTransitionTimeWhenChanged(t *testing.T) {
	stable := metav1.Now()
	existing := []metav1.Condition{{
		Type:               conditions.Ready,
		Status:             metav1.ConditionFalse,
		Reason:             "Recasting",
		Message:            "Minted service run echo-service-1.",
		LastTransitionTime: stable,
	}}

	time.Sleep(time.Millisecond)
	merged := conditions.Merge(existing, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionTrue,
		Reason:  "RunActive",
		Message: "Service run echo-service-1 is active.",
	})
	if merged[0].LastTransitionTime.Equal(&stable) {
		t.Fatalf("expected transition time to advance")
	}
}

func TestForAgentRunPhaseStableWhileRunning(t *testing.T) {
	stable := metav1.Now()
	existing := []metav1.Condition{{
		Type:               conditions.Progressing,
		Status:             metav1.ConditionTrue,
		Reason:             "Running",
		Message:            "Agent run pod is executing.",
		LastTransitionTime: stable,
	}}

	merged := conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseRunning, existing)
	for _, condition := range merged {
		if condition.Type == conditions.Progressing && !condition.LastTransitionTime.Equal(&stable) {
			t.Fatalf("expected progressing transition time to remain stable")
		}
	}
}

func TestMergeSortsByType(t *testing.T) {
	merged := conditions.Merge(nil,
		metav1.Condition{Type: conditions.Progressing, Status: metav1.ConditionTrue, Reason: "Running", Message: "active"},
		metav1.Condition{Type: conditions.Ready, Status: metav1.ConditionTrue, Reason: "RunActive", Message: "ready"},
	)
	if merged[0].Type != conditions.Progressing || merged[1].Type != conditions.Ready {
		t.Fatalf("unexpected order: %s, %s", merged[0].Type, merged[1].Type)
	}
}

func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}

func TestUnsupportedMode(t *testing.T) {
	conds := conditions.UnsupportedMode("Task")
	if len(conds) != 2 {
		t.Fatalf("expected two conditions, got %d", len(conds))
	}

	ready := findCondition(conds, conditions.Ready)
	if ready == nil {
		t.Fatalf("expected Ready condition")
	}
	if ready.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %s", ready.Status)
	}
	if ready.Reason != "UnsupportedMode" {
		t.Fatalf("unexpected Ready reason: %s", ready.Reason)
	}
	if !strings.Contains(ready.Message, "Task") {
		t.Fatalf("expected mode in message, got %q", ready.Message)
	}

	progressing := findCondition(conds, conditions.Progressing)
	if progressing == nil {
		t.Fatalf("expected Progressing condition")
	}
	if progressing.Status != metav1.ConditionFalse {
		t.Fatalf("expected Progressing=False, got %s", progressing.Status)
	}
	if progressing.Reason != "UnsupportedMode" {
		t.Fatalf("unexpected Progressing reason: %s", progressing.Reason)
	}
}

func TestInvalidMode(t *testing.T) {
	conds := conditions.InvalidMode("Bogus")
	if len(conds) != 1 {
		t.Fatalf("expected one condition, got %d", len(conds))
	}
	if conds[0].Type != conditions.Ready {
		t.Fatalf("expected Ready condition, got %s", conds[0].Type)
	}
	if conds[0].Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %s", conds[0].Status)
	}
	if conds[0].Reason != "InvalidMode" {
		t.Fatalf("unexpected reason: %s", conds[0].Reason)
	}
	if !strings.Contains(conds[0].Message, "Bogus") {
		t.Fatalf("expected mode in message, got %q", conds[0].Message)
	}
}

func TestBudgetConfiguredValid(t *testing.T) {
	condition := conditions.BudgetConfigured(true, "ignored when valid")
	if condition.Type != conditions.BudgetValid {
		t.Fatalf("unexpected type: %s", condition.Type)
	}
	if condition.Status != metav1.ConditionTrue {
		t.Fatalf("expected True, got %s", condition.Status)
	}
	if condition.Reason != "Configured" {
		t.Fatalf("unexpected reason: %s", condition.Reason)
	}
}

func TestBudgetConfiguredInvalidUsesMessage(t *testing.T) {
	condition := conditions.BudgetConfigured(false, "bad wallclock")
	if condition.Status != metav1.ConditionFalse {
		t.Fatalf("expected False, got %s", condition.Status)
	}
	if condition.Reason != "InvalidWallclock" {
		t.Fatalf("unexpected reason: %s", condition.Reason)
	}
	if condition.Message != "bad wallclock" {
		t.Fatalf("expected supplied message, got %q", condition.Message)
	}
}

func TestForAgentRunPhaseTerminal(t *testing.T) {
	merged := conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseSucceeded, nil)

	complete := findCondition(merged, conditions.Complete)
	if complete == nil {
		t.Fatalf("expected Complete condition")
	}
	if complete.Status != metav1.ConditionTrue {
		t.Fatalf("expected Complete=True for terminal phase, got %s", complete.Status)
	}
	if complete.Reason != string(kontextv1alpha1.AgentRunPhaseSucceeded) {
		t.Fatalf("unexpected Complete reason: %s", complete.Reason)
	}

	progressing := findCondition(merged, conditions.Progressing)
	if progressing == nil {
		t.Fatalf("expected Progressing condition")
	}
	if progressing.Status != metav1.ConditionFalse {
		t.Fatalf("expected Progressing=False for terminal phase, got %s", progressing.Status)
	}
}

func TestForAgentRunPhasePendingDefault(t *testing.T) {
	merged := conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhasePending, nil)

	complete := findCondition(merged, conditions.Complete)
	if complete == nil || complete.Status != metav1.ConditionFalse {
		t.Fatalf("expected Complete=False for pending phase")
	}
	progressing := findCondition(merged, conditions.Progressing)
	if progressing == nil || progressing.Status != metav1.ConditionTrue {
		t.Fatalf("expected Progressing=True for pending phase")
	}
	if progressing.Reason != string(kontextv1alpha1.AgentRunPhasePending) {
		t.Fatalf("unexpected Progressing reason: %s", progressing.Reason)
	}
}
