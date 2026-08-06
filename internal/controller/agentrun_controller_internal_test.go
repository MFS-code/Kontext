package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestAgentRunControllerPredicatesSeparateWorkerPools(t *testing.T) {
	podBacked := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-backed"},
	}
	delivered := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "delivered"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Delivery: &kontextv1alpha1.AgentRunDeliverySpec{Port: 8080},
		},
	}

	if !isPodBackedAgentRun(podBacked) || isDeliveredAgentRun(podBacked) {
		t.Fatal("Pod-backed run did not select only the Pod worker pool")
	}
	if isPodBackedAgentRun(delivered) || !isDeliveredAgentRun(delivered) {
		t.Fatal("delivered run did not select only the delivery worker pool")
	}
}

func TestDeliveryDeadlineUsesTheEarlierLimit(t *testing.T) {
	created := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	limit := time.Minute
	reconciler := &AgentRunReconciler{}
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
	}

	deadline, budget := reconciler.deliveryDeadline(run, &limit, created)
	if budget || !deadline.Equal(created.Add(defaultDeliveryTimeout)) {
		t.Fatalf("delivery window deadline = %s budget=%t", deadline, budget)
	}

	started := metav1.NewTime(created)
	run.Status.StartTime = &started
	deadline, budget = reconciler.deliveryDeadline(run, &limit, created)
	if !budget || !deadline.Equal(created.Add(limit)) {
		t.Fatalf("wallclock deadline = %s budget=%t", deadline, budget)
	}

	lateStart := metav1.NewTime(created.Add(4*time.Minute + 30*time.Second))
	run.Status.StartTime = &lateStart
	deadline, budget = reconciler.deliveryDeadline(run, &limit, created)
	if budget || !deadline.Equal(created.Add(defaultDeliveryTimeout)) {
		t.Fatalf("earlier delivery deadline = %s budget=%t", deadline, budget)
	}
}
