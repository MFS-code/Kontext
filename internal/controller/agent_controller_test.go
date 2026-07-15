package controller_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/conditions"
	"github.com/kontext-dev/kontext/internal/podbuilder"
)

func TestAgentReconcilerMintsServiceRun(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mint-owner",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:     kontextv1alpha1.AgentModeService,
			Goal:     "stay ready",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
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
	if updated.Status.CurrentRunName != "mint-owner-1" {
		t.Fatalf("expected mint-owner-1, got %q", updated.Status.CurrentRunName)
	}
	if updated.Status.RunsCreated != 1 {
		t.Fatalf("expected runsCreated=1, got %d", updated.Status.RunsCreated)
	}

	var run kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "mint-owner-1", Namespace: agent.Namespace}, &run); err != nil {
		t.Fatalf("expected child run: %v", err)
	}
}

func TestAgentReconcilerNoopsWhenRunActive(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "active-owner",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:     kontextv1alpha1.AgentModeService,
			Goal:     "stay ready",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := updateAgentStatus(ctx, agent, kontextv1alpha1.AgentStatus{
		CurrentRunName: "active-owner-1",
		RunsCreated:    1,
	}); err != nil {
		t.Fatalf("update agent status: %v", err)
	}

	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "active-owner-1",
			Namespace: "default",
			Labels:    map[string]string{podbuilder.LabelAgentName: agent.Name},
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef: &kontextv1alpha1.AgentRef{Name: agent.Name},
			Goal:     agent.Spec.Goal,
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase: kontextv1alpha1.AgentRunPhaseRunning,
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	var runs kontextv1alpha1.AgentRunList
	if err := k8sClient.List(ctx, &runs, client.MatchingLabels{podbuilder.LabelAgentName: agent.Name}); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected one run, got %d", len(runs.Items))
	}
}

func TestAgentReconcilerRecastsAfterTerminalRun(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "recast-owner",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:     kontextv1alpha1.AgentModeService,
			Goal:     "stay ready",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
			Backoff:  &kontextv1alpha1.BackoffSpec{InitialSeconds: 1, MaxSeconds: 1},
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := updateAgentStatus(ctx, agent, kontextv1alpha1.AgentStatus{
		CurrentRunName: "recast-owner-1",
		RunsCreated:    1,
		Restarts:       0,
	}); err != nil {
		t.Fatalf("update agent status: %v", err)
	}

	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "recast-owner-1",
			Namespace: "default",
			Labels:    map[string]string{podbuilder.LabelAgentName: agent.Name},
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef: &kontextv1alpha1.AgentRef{Name: agent.Name},
			Goal:     agent.Spec.Goal,
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase:          kontextv1alpha1.AgentRunPhaseFailed,
		CompletionTime: &metav1.Time{Time: time.Now().Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != "recast-owner-2" {
		t.Fatalf("expected recast-owner-2, got %q", updated.Status.CurrentRunName)
	}
	if updated.Status.RunsCreated != 2 {
		t.Fatalf("expected runsCreated=2, got %d", updated.Status.RunsCreated)
	}
}

func TestAgentReconcilerRecastsWhenCurrentRunMissing(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "missing-run-owner",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:     kontextv1alpha1.AgentModeService,
			Goal:     "stay ready",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := updateAgentStatus(ctx, agent, kontextv1alpha1.AgentStatus{
		CurrentRunName: "missing-run-owner-1",
		RunsCreated:    1,
	}); err != nil {
		t.Fatalf("update agent status: %v", err)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != "missing-run-owner-2" {
		t.Fatalf("expected missing-run-owner-2, got %q", updated.Status.CurrentRunName)
	}
	if updated.Status.RunsCreated != 2 {
		t.Fatalf("expected runsCreated=2, got %d", updated.Status.RunsCreated)
	}

	var run kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "missing-run-owner-2", Namespace: agent.Namespace}, &run); err != nil {
		t.Fatalf("expected replacement run: %v", err)
	}
}

func TestAgentReconcilerAlreadyExistsDoesNotBumpCounters(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "exists-owner",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:     kontextv1alpha1.AgentModeService,
			Goal:     "stay ready",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "exists-owner-1",
			Namespace: "default",
			Labels:    map[string]string{podbuilder.LabelAgentName: agent.Name},
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef: &kontextv1alpha1.AgentRef{Name: agent.Name},
			Goal:     agent.Spec.Goal,
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != "exists-owner-1" {
		t.Fatalf("expected current run exists-owner-1, got %q", updated.Status.CurrentRunName)
	}
	if updated.Status.RunsCreated != 0 {
		t.Fatalf("expected runsCreated to remain 0, got %d", updated.Status.RunsCreated)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent after second reconcile: %v", err)
	}
	if updated.Status.RunsCreated != 0 {
		t.Fatalf("expected runsCreated to remain 0 after duplicate reconcile, got %d", updated.Status.RunsCreated)
	}
}

func TestAgentReconcilerUnsupportedMode(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-agent",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:    kontextv1alpha1.AgentModeTask,
			Goal:    "one shot",
			Model:   "echo-model",
			Runtime: echoRuntimeSpec(),
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
	found := false
	for _, condition := range updated.Status.Conditions {
		if condition.Type == conditions.Ready && condition.Reason == "UnsupportedMode" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected UnsupportedMode condition, got %#v", updated.Status.Conditions)
	}
}

func updateAgentStatus(ctx context.Context, agent *kontextv1alpha1.Agent, next kontextv1alpha1.AgentStatus) error {
	agent.Status = next
	return k8sClient.Status().Update(ctx, agent)
}
