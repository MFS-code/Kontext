package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
)

func TestSetStatusConditionsPreservesTransitionTimeForReasonAndMessageChanges(t *testing.T) {
	stable := metav1.NewTime(time.Unix(1, 0))
	conditions := []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: 1,
		LastTransitionTime: stable,
		Reason:             "OldReason",
		Message:            "old message",
	}, {
		Type:               "Unchanged",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: 1,
		LastTransitionTime: stable,
		Reason:             "StillPresent",
		Message:            "preserve me",
	}}

	setStatusConditions(&conditions, 7, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "NewReason",
		Message: "old message",
	})

	if len(conditions) != 2 {
		t.Fatalf("expected untouched conditions to be preserved, got %#v", conditions)
	}
	ready := findStatusCondition(conditions, "Ready")
	if ready == nil {
		t.Fatal("expected Ready condition")
	}
	if !ready.LastTransitionTime.Equal(&stable) {
		t.Fatalf("reason/message-only update changed transition time to %s", ready.LastTransitionTime)
	}
	if ready.ObservedGeneration != 7 {
		t.Fatalf("expected observed generation 7, got %d", ready.ObservedGeneration)
	}
	if ready.Reason != "NewReason" || ready.Message != "old message" {
		t.Fatalf("condition update was not applied: %#v", ready)
	}
	if findStatusCondition(conditions, "Unchanged") == nil {
		t.Fatal("expected condition not included in updates to remain")
	}

	setStatusConditions(&conditions, 8, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "NewReason",
		Message: "new message",
	})
	ready = findStatusCondition(conditions, "Ready")
	if ready == nil {
		t.Fatal("expected Ready condition after message update")
	}
	if !ready.LastTransitionTime.Equal(&stable) {
		t.Fatalf("message-only update changed transition time to %s", ready.LastTransitionTime)
	}
	if ready.ObservedGeneration != 8 {
		t.Fatalf("expected observed generation 8, got %d", ready.ObservedGeneration)
	}
}

func TestSetStatusConditionsAdvancesTransitionTimeWhenStatusChanges(t *testing.T) {
	stable := metav1.NewTime(time.Unix(1, 0))
	conditions := []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: 2,
		LastTransitionTime: stable,
		Reason:             "Starting",
		Message:            "starting",
	}}

	setStatusConditions(&conditions, 3, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "Running",
		Message: "running",
	})

	ready := findStatusCondition(conditions, "Ready")
	if ready == nil {
		t.Fatal("expected Ready condition")
	}
	if ready.LastTransitionTime.Equal(&stable) {
		t.Fatal("status change did not advance transition time")
	}
	if ready.ObservedGeneration != 3 {
		t.Fatalf("expected observed generation 3, got %d", ready.ObservedGeneration)
	}
}

func TestEnforceWallclockTreatsOmissionAsNoDeadline(t *testing.T) {
	reconciler := &AgentRunReconciler{}
	runs := []*kontextv1alpha1.AgentRun{
		{},
		{Spec: kontextv1alpha1.AgentRunSpec{Budget: &kontextv1alpha1.BudgetSpec{}}},
	}

	for _, run := range runs {
		result, done, err := reconciler.enforceWallclock(context.Background(), run, &corev1.Pod{})
		if err != nil {
			t.Fatalf("omitted wallclock returned error: %v", err)
		}
		if done || result.Requeue || result.RequeueAfter != 0 {
			t.Fatalf("omitted wallclock scheduled enforcement: result=%#v done=%t", result, done)
		}
		if len(run.Status.Conditions) != 0 {
			t.Fatalf("omitted wallclock wrote conditions: %#v", run.Status.Conditions)
		}
	}
}

func findStatusCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
