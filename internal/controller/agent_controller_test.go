package controller_test

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/conditions"
	"github.com/kontext-dev/kontext/internal/controller"
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
			Env: []kontextv1alpha1.EnvVar{{
				Name: "MCP_TOKEN",
				ValueFrom: &kontextv1alpha1.EnvVarSource{
					SecretKeyRef: kontextv1alpha1.SecretKeySelector{Name: "mcp-auth", Key: "token"},
				},
			}},
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:   "kontext-echo:dev",
				Command: []string{"/entrypoint.sh"},
				Result: &kontextv1alpha1.RuntimeResultSpec{
					Source: kontextv1alpha1.ResultSourceStdout,
					Format: kontextv1alpha1.ResultFormatLastLine,
				},
			},
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
	if run.Spec.Runtime.Result == nil ||
		run.Spec.Runtime.Result.Source != kontextv1alpha1.ResultSourceStdout ||
		run.Spec.Runtime.Result.Format != kontextv1alpha1.ResultFormatLastLine {
		t.Fatalf("runtime result policy was not snapshotted: %#v", run.Spec.Runtime.Result)
	}
	if len(run.Spec.Runtime.Command) != 1 || run.Spec.Runtime.Command[0] != "/entrypoint.sh" {
		t.Fatalf("runtime command was not snapshotted: %#v", run.Spec.Runtime.Command)
	}
	if len(run.Spec.Env) != 1 || run.Spec.Env[0].ValueFrom == nil ||
		run.Spec.Env[0].ValueFrom.SecretKeyRef.Name != "mcp-auth" ||
		run.Spec.Env[0].ValueFrom.SecretKeyRef.Key != "token" {
		t.Fatalf("Secret-backed env was not snapshotted: %#v", run.Spec.Env)
	}
	updated.Spec.Env[0].ValueFrom.SecretKeyRef.Name = "changed-auth"
	updated.Spec.Env[0].ValueFrom.SecretKeyRef.Key = "changed-token"
	if err := k8sClient.Update(ctx, &updated); err != nil {
		t.Fatalf("update Agent Secret ref: %v", err)
	}
	if err := k8sClient.Get(
		ctx,
		types.NamespacedName{Name: run.Name, Namespace: run.Namespace},
		&run,
	); err != nil {
		t.Fatalf("get child run after Agent update: %v", err)
	}
	if run.Spec.Env[0].ValueFrom.SecretKeyRef.Name != "mcp-auth" ||
		run.Spec.Env[0].ValueFrom.SecretKeyRef.Key != "token" {
		t.Fatalf("child run Secret ref drifted with Agent update: %#v", run.Spec.Env)
	}
}

func TestAgentReconcilerRequeuesWhenCachedListMissesOwnedRun(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stale-list-owner",
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
			Name:      "stale-list-owner-1",
			Namespace: agent.Namespace,
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
	createOwnedAgentRun(ctx, t, agent, run)

	staleClient := &staleAgentRunListClient{
		Client:   k8sClient,
		omitName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace},
	}
	reconciler := &controller.AgentReconciler{
		Client:    staleClient,
		APIReader: k8sClient,
		Scheme:    scheme,
	}
	result, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile with stale List: %v", err)
	}
	if !result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("expected clean immediate requeue, got %#v", result)
	}

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != "" || updated.Status.RunsCreated != 0 {
		t.Fatalf("stale-list recovery mutated status: %#v", updated.Status)
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace},
	}); err != nil {
		t.Fatalf("follow-up reconcile: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent after follow-up: %v", err)
	}
	if updated.Status.CurrentRunName != run.Name || updated.Status.RunsCreated != 1 {
		t.Fatalf("follow-up did not observe existing run: %#v", updated.Status)
	}

	var runs kontextv1alpha1.AgentRunList
	if err := k8sClient.List(ctx, &runs, client.InNamespace(agent.Namespace), client.MatchingLabels{
		podbuilder.LabelAgentName: agent.Name,
	}); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected one owned run after stale-list recovery, got %d", len(runs.Items))
	}
}

func TestAgentReconcilerRejectsUnrelatedRunNameCollision(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "collision-owner",
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

	unrelated := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "collision-owner-1",
			Namespace: agent.Namespace,
			Labels:    map[string]string{podbuilder.LabelAgentName: agent.Name},
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "unrelated work",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
		},
	}
	if err := k8sClient.Create(ctx, unrelated); err != nil {
		t.Fatalf("create unrelated run: %v", err)
	}

	_, err := newAgentReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace},
	})
	if err == nil || !strings.Contains(err.Error(), "service run name collision") {
		t.Fatalf("expected explicit name collision, got %v", err)
	}

	var existing kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: unrelated.Name, Namespace: unrelated.Namespace}, &existing); err != nil {
		t.Fatalf("get unrelated run: %v", err)
	}
	if len(existing.OwnerReferences) != 0 || existing.Spec.AgentRef != nil {
		t.Fatalf("unrelated run was adopted or mutated: %#v", existing)
	}

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != "" || updated.Status.RunsCreated != 0 {
		t.Fatalf("collision mutated Agent status: %#v", updated.Status)
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
	createOwnedAgentRun(ctx, t, agent, run)
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

func TestAgentReconcilerRecastsTerminalRunWithStaleStatus(t *testing.T) {
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
	createOwnedAgentRun(ctx, t, agent, run)
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
	if updated.Status.LastRunName != "recast-owner-1" {
		t.Fatalf("expected lastRunName recast-owner-1, got %q", updated.Status.LastRunName)
	}
	if updated.Status.RunsCreated != 2 {
		t.Fatalf("expected runsCreated=2, got %d", updated.Status.RunsCreated)
	}

	// A follow-up reconcile with the new run active must keep the history intact.
	var newRun kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "recast-owner-2", Namespace: agent.Namespace}, &newRun); err != nil {
		t.Fatalf("get new run: %v", err)
	}
	if err := updateAgentRunStatus(ctx, &newRun, kontextv1alpha1.AgentRunStatus{
		Phase: kontextv1alpha1.AgentRunPhaseRunning,
	}); err != nil {
		t.Fatalf("update new run status: %v", err)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != "recast-owner-2" {
		t.Fatalf("expected recast-owner-2 to stay current, got %q", updated.Status.CurrentRunName)
	}
	if updated.Status.LastRunName != "recast-owner-1" {
		t.Fatalf("expected lastRunName to remain recast-owner-1, got %q", updated.Status.LastRunName)
	}
}

func TestAgentReconcilerRecastsTerminalRunWithoutCompletionTime(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "missing-completion-owner",
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
			Name:      "missing-completion-owner-1",
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
	createOwnedAgentRun(ctx, t, agent, run)
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase: kontextv1alpha1.AgentRunPhaseFailed,
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	result, err := newAgentReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile agent: %v", err)
	}
	if result.RequeueAfter != 2*time.Second {
		t.Fatalf("expected normal post-create requeue, got %s", result.RequeueAfter)
	}

	var runs kontextv1alpha1.AgentRunList
	if err := k8sClient.List(ctx, &runs, client.MatchingLabels{podbuilder.LabelAgentName: agent.Name}); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 2 {
		t.Fatalf("expected immediate recast, got %d runs", len(runs.Items))
	}

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != "missing-completion-owner-2" {
		t.Fatalf("expected missing-completion-owner-2, got %q", updated.Status.CurrentRunName)
	}
}

func TestAgentReconcilerBacksOffFromObservedTerminalRun(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backoff-owner",
			Namespace: "default",
		},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:     kontextv1alpha1.AgentModeService,
			Goal:     "stay ready",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
			Backoff:  &kontextv1alpha1.BackoffSpec{InitialSeconds: 30, MaxSeconds: 30},
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backoff-owner-1",
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
	createOwnedAgentRun(ctx, t, agent, run)
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase:          kontextv1alpha1.AgentRunPhaseFailed,
		CompletionTime: &metav1.Time{Time: time.Now()},
	}); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	result, err := newAgentReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile agent: %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > 30*time.Second {
		t.Fatalf("expected remaining 30-second backoff, got %s", result.RequeueAfter)
	}

	var runs kontextv1alpha1.AgentRunList
	if err := k8sClient.List(ctx, &runs, client.MatchingLabels{podbuilder.LabelAgentName: agent.Name}); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected one run during backoff, got %d", len(runs.Items))
	}

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != run.Name || updated.Status.RunsCreated != 1 {
		t.Fatalf("status was not recovered during backoff: %#v", updated.Status)
	}
}

func TestAgentReconcilerIgnoresStaleStatusWhenNoChildrenExist(t *testing.T) {
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
	if updated.Status.CurrentRunName != "missing-run-owner-1" {
		t.Fatalf("expected missing-run-owner-1, got %q", updated.Status.CurrentRunName)
	}
	if updated.Status.RunsCreated != 1 {
		t.Fatalf("expected runsCreated=1, got %d", updated.Status.RunsCreated)
	}

	var run kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "missing-run-owner-1", Namespace: agent.Namespace}, &run); err != nil {
		t.Fatalf("expected first observed run: %v", err)
	}
}

func TestAgentReconcilerRecoversStatusFromObservedChildren(t *testing.T) {
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
	createOwnedAgentRun(ctx, t, agent, run)

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != "exists-owner-1" {
		t.Fatalf("expected current run exists-owner-1, got %q", updated.Status.CurrentRunName)
	}
	if updated.Status.RunsCreated != 1 {
		t.Fatalf("expected runsCreated to recover to 1, got %d", updated.Status.RunsCreated)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent after second reconcile: %v", err)
	}
	if updated.Status.RunsCreated != 1 {
		t.Fatalf("expected runsCreated to remain 1 after duplicate reconcile, got %d", updated.Status.RunsCreated)
	}
}

func TestAgentReconcilerUsesOwnedCanonicalRunSequence(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sequence-owner",
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

	unrelated := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sequence-owner-99",
			Namespace: agent.Namespace,
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
	if err := k8sClient.Create(ctx, unrelated); err != nil {
		t.Fatalf("create unrelated same-label run: %v", err)
	}

	malformed := unrelated.DeepCopy()
	malformed.ObjectMeta = metav1.ObjectMeta{
		Name:      "sequence-owner-02",
		Namespace: agent.Namespace,
		Labels:    map[string]string{podbuilder.LabelAgentName: agent.Name},
	}
	createOwnedAgentRun(ctx, t, agent, malformed)

	current := unrelated.DeepCopy()
	current.ObjectMeta = metav1.ObjectMeta{
		Name:      "sequence-owner-3",
		Namespace: agent.Namespace,
		Labels:    map[string]string{podbuilder.LabelAgentName: agent.Name},
	}
	createOwnedAgentRun(ctx, t, agent, current)
	if err := updateAgentRunStatus(ctx, current, kontextv1alpha1.AgentRunStatus{
		Phase:          kontextv1alpha1.AgentRunPhaseFailed,
		CompletionTime: &metav1.Time{Time: time.Now().Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("update current run status: %v", err)
	}

	reconcileAgent(ctx, t, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace})

	var next kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "sequence-owner-4", Namespace: agent.Namespace}, &next); err != nil {
		t.Fatalf("expected next canonical run after gap: %v", err)
	}

	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &updated); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if updated.Status.CurrentRunName != next.Name ||
		updated.Status.LastRunName != current.Name ||
		updated.Status.RunsCreated != 4 {
		t.Fatalf("unexpected status from observed sequence: %#v", updated.Status)
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

func createOwnedAgentRun(
	ctx context.Context,
	t *testing.T,
	agent *kontextv1alpha1.Agent,
	run *kontextv1alpha1.AgentRun,
) {
	t.Helper()
	if err := controllerutil.SetControllerReference(agent, run, scheme); err != nil {
		t.Fatalf("set AgentRun owner: %v", err)
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
}

type staleAgentRunListClient struct {
	client.Client
	omitName types.NamespacedName
}

func (c *staleAgentRunListClient) List(
	ctx context.Context,
	list client.ObjectList,
	opts ...client.ListOption,
) error {
	runs, ok := list.(*kontextv1alpha1.AgentRunList)
	if !ok || c.omitName.Name == "" {
		return c.Client.List(ctx, list, opts...)
	}
	if err := c.Client.List(ctx, runs, opts...); err != nil {
		return err
	}
	filtered := runs.Items[:0]
	for i := range runs.Items {
		run := runs.Items[i]
		if run.Name == c.omitName.Name && run.Namespace == c.omitName.Namespace {
			continue
		}
		filtered = append(filtered, run)
	}
	runs.Items = filtered
	c.omitName = types.NamespacedName{}
	return nil
}
