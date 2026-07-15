package conditions_test

import (
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
		Message:            "Service run echo-owner-1 is active.",
		LastTransitionTime: stable,
	}}

	merged := conditions.Merge(existing, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionTrue,
		Reason:  "RunActive",
		Message: "Service run echo-owner-1 is active.",
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
		Message:            "Minted service run echo-owner-1.",
		LastTransitionTime: stable,
	}}

	time.Sleep(time.Millisecond)
	merged := conditions.Merge(existing, metav1.Condition{
		Type:    conditions.Ready,
		Status:  metav1.ConditionTrue,
		Reason:  "RunActive",
		Message: "Service run echo-owner-1 is active.",
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
