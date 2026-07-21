package conditions_test

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
)

func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
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

func TestForAgentRunPhaseTerminal(t *testing.T) {
	merged := conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhaseSucceeded)

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
	merged := conditions.ForAgentRunPhase(kontextv1alpha1.AgentRunPhasePending)

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
