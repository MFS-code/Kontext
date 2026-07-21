package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/podbuilder"
)

func TestAgentReconcilerProjectsTaskReadinessAndRetainedChildren(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-agent",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:         kontextv1alpha1.AgentModeTask,
			GoalTemplate: "Review ${area}.",
			Model:        "echo-model",
			Runtime:      echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.RunsCreated != 0 ||
		updated.Status.LastRunName != "" ||
		updated.Status.CurrentRunName != "" ||
		updated.Status.Restarts != 0 ||
		updated.Status.ObservedGeneration != updated.Generation {
		t.Fatalf("new Task Agent minted or projected a run: %#v", updated.Status)
	}
	ready := false
	for _, condition := range updated.Status.Conditions {
		if condition.Type == conditions.Ready &&
			condition.Status == metav1.ConditionTrue &&
			condition.Reason == "TemplateReady" &&
			condition.ObservedGeneration == updated.Generation {
			ready = true
		}
	}
	if !ready {
		t.Fatalf("expected ready Task template, got %#v", updated.Status.Conditions)
	}

	first := taskRunForAgent(agent, "a-first")
	createOwnedAgentRun(ctx, t, agent, first)
	second := taskRunForAgent(agent, "m-second")
	createOwnedAgentRun(ctx, t, agent, second)

	ownedWithoutLabel := taskRunForAgent(agent, "z-owned-without-label")
	ownedWithoutLabel.Labels = nil
	createOwnedAgentRun(ctx, t, agent, ownedWithoutLabel)

	unrelated := taskRunForAgent(agent, "unrelated-same-label")
	if err := k8sClient.Create(ctx, unrelated); err != nil {
		t.Fatalf("create unrelated same-label run: %v", err)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get projected Task Agent: %v", err)
	}
	if updated.Status.RunsCreated != 3 ||
		updated.Status.LastRunName != ownedWithoutLabel.Name ||
		updated.Status.CurrentRunName != "" ||
		updated.Status.Restarts != 0 {
		t.Fatalf("unexpected concurrent Task projection: %#v", updated.Status)
	}

	if err := k8sClient.Delete(ctx, ownedWithoutLabel); err != nil {
		t.Fatalf("delete newest retained Task run: %v", err)
	}
	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get Task Agent after retained deletion: %v", err)
	}
	if updated.Status.RunsCreated != 2 || updated.Status.LastRunName != second.Name {
		t.Fatalf("retained Task projection did not decrease exactly: %#v", updated.Status)
	}

	updated.Spec.GoalTemplate = "Review ${area} for ${"
	if err := k8sClient.Update(ctx, &updated); err != nil {
		t.Fatalf("update Task template: %v", err)
	}
	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get invalid Task Agent status: %v", err)
	}
	if updated.Status.ObservedGeneration != updated.Generation {
		t.Fatalf(
			"observed generation = %d, want %d",
			updated.Status.ObservedGeneration,
			updated.Generation,
		)
	}
	for _, condition := range updated.Status.Conditions {
		if condition.Type == conditions.Ready &&
			condition.Status == metav1.ConditionFalse &&
			condition.Reason == "InvalidTemplate" {
			return
		}
	}
	t.Fatalf("malformed Task template remained Ready: %#v", updated.Status.Conditions)
}

func taskRunForAgent(
	agent *kontextv1alpha1.Agent,
	name string,
) *kontextv1alpha1.AgentRun {
	return &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: agent.Namespace,
			Labels: map[string]string{
				podbuilder.LabelAgentName: agent.Name,
			},
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef:   &kontextv1alpha1.AgentRef{Name: agent.Name},
			Parameters: map[string]string{"area": name},
			Goal:       "Review " + name + ".",
			Model:      agent.Spec.Model,
			Runtime:    agent.Spec.Runtime,
		},
	}
}
