package controller_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/controller"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/scheduledrun"
	"github.com/MFS-code/Kontext/internal/scheduler"
)

type fakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func TestScheduledFirstFutureFireAndStatus(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 30, 0, time.UTC)}
	agent := createScheduledAgent(ctx, t, "scheduled-first", "* * * * *", nil)
	reconciler := newScheduledReconciler(clock)

	result := reconcileScheduled(ctx, t, reconciler, agent)
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("initial requeue = %s, want 30s", result.RequeueAfter)
	}
	updated := getAgent(ctx, t, agent)
	assertTime(t, updated.Status.NextScheduleTime, time.Date(2026, time.July, 20, 12, 1, 0, 0, time.UTC))
	if updated.Status.LastRunName != "" || updated.Status.RunsCreated != 0 {
		t.Fatalf("first reconciliation created a run: %#v", updated.Status)
	}

	clock.Set(time.Date(2026, time.July, 20, 12, 1, 10, 0, time.UTC))
	result = reconcileScheduled(ctx, t, reconciler, agent)
	if result.RequeueAfter != 50*time.Second {
		t.Fatalf("post-create requeue = %s, want 50s", result.RequeueAfter)
	}

	updated = getAgent(ctx, t, agent)
	wantRunName := scheduler.RunName(agent.Name, time.Date(2026, time.July, 20, 12, 1, 0, 0, time.UTC))
	if updated.Status.LastRunName != wantRunName ||
		updated.Status.RunsCreated != 1 ||
		updated.Status.CurrentRunName != "" ||
		updated.Status.Restarts != 0 ||
		updated.Status.ObservedGeneration != updated.Generation {
		t.Fatalf("unexpected scheduled status: %#v", updated.Status)
	}
	assertTime(t, updated.Status.LastScheduleTime, time.Date(2026, time.July, 20, 12, 1, 0, 0, time.UTC))
	assertTime(t, updated.Status.NextScheduleTime, time.Date(2026, time.July, 20, 12, 2, 0, 0, time.UTC))
	assertCondition(
		t,
		updated.Status.Conditions,
		conditions.Ready,
		metav1.ConditionTrue,
		"RunCreated",
		time.Date(2026, time.July, 20, 12, 0, 30, 0, time.UTC),
	)
	assertCondition(t, updated.Status.Conditions, conditions.Progressing, metav1.ConditionTrue, "RunCreated", clock.Now())

	var run kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: wantRunName, Namespace: agent.Namespace}, &run); err != nil {
		t.Fatalf("get scheduled run: %v", err)
	}
	slot, slotOK := scheduledrun.Slot(&run)
	sequence, sequenceOK := scheduledrun.Sequence(&run)
	if run.Spec.Goal != agent.Spec.Goal ||
		!slotOK ||
		!slot.Equal(time.Date(2026, time.July, 20, 12, 1, 0, 0, time.UTC)) ||
		!sequenceOK ||
		sequence != 1 ||
		!metav1.IsControlledBy(&run, agent) {
		t.Fatalf("scheduled snapshot metadata is incomplete: %#v", run)
	}

	for range 20 {
		reconcileScheduled(ctx, t, reconciler, agent)
	}
	if got := listOwnedRuns(ctx, t, agent); len(got) != 1 {
		t.Fatalf("idempotent retries created %d runs", len(got))
	}
}

func TestScheduledLatestWithinDeadlineAndNoBackfill(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
	agent := createScheduledAgent(ctx, t, "scheduled-jitter", "* * * * *", nil)
	reconciler := newScheduledReconciler(clock)
	reconcileScheduled(ctx, t, reconciler, agent)

	clock.Set(time.Date(2026, time.July, 20, 12, 5, 20, 0, time.UTC))
	reconcileScheduled(ctx, t, reconciler, agent)
	runs := listOwnedRuns(ctx, t, agent)
	if len(runs) != 1 {
		t.Fatalf("expected one latest due slot, got %#v", runs)
	}
	slot, slotOK := scheduledrun.Slot(&runs[0])
	if !slotOK ||
		!slot.Equal(time.Date(2026, time.July, 20, 12, 5, 0, 0, time.UTC)) {
		t.Fatalf("expected only latest due slot, got %#v", runs)
	}

	deadline := int64(60)
	hourly := createScheduledAgent(ctx, t, "scheduled-expired", "0 * * * *", &kontextv1alpha1.ScheduleSpec{
		Expression:              "0 * * * *",
		StartingDeadlineSeconds: &deadline,
	})
	clock.Set(time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC))
	reconcileScheduled(ctx, t, reconciler, hourly)
	clock.Set(time.Date(2026, time.July, 20, 13, 2, 0, 0, time.UTC))
	reconcileScheduled(ctx, t, reconciler, hourly)
	if runs := listOwnedRuns(ctx, t, hourly); len(runs) != 0 {
		t.Fatalf("expired slot created %d runs", len(runs))
	}
	updated := getAgent(ctx, t, hourly)
	assertCondition(
		t,
		updated.Status.Conditions,
		conditions.Progressing,
		metav1.ConditionFalse,
		"MissedDeadline",
		time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC),
	)
	assertTime(t, updated.Status.NextScheduleTime, time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC))
}

func TestScheduledGenerationChangeResetsAnchor(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
	agent := createScheduledAgent(ctx, t, "scheduled-edit", "* * * * *", nil)
	reconciler := newScheduledReconciler(clock)
	reconcileScheduled(ctx, t, reconciler, agent)

	clock.Set(time.Date(2026, time.July, 20, 12, 1, 5, 0, time.UTC))
	updated := getAgent(ctx, t, agent)
	updated.Spec.Schedule.Expression = "*/2 * * * *"
	if err := k8sClient.Update(ctx, updated); err != nil {
		t.Fatalf("update schedule: %v", err)
	}
	reconcileScheduled(ctx, t, reconciler, agent)
	if runs := listOwnedRuns(ctx, t, agent); len(runs) != 0 {
		t.Fatalf("schedule edit backfilled %d runs", len(runs))
	}
	updated = getAgent(ctx, t, agent)
	assertTime(t, updated.Status.NextScheduleTime, time.Date(2026, time.July, 20, 12, 2, 0, 0, time.UTC))
	assertCondition(
		t,
		updated.Status.Conditions,
		conditions.Progressing,
		metav1.ConditionFalse,
		"ScheduleUpdated",
		time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC),
	)
}

func TestScheduledConcurrencyPolicies(t *testing.T) {
	for _, phase := range []kontextv1alpha1.AgentRunPhase{
		kontextv1alpha1.AgentRunPhasePending,
		kontextv1alpha1.AgentRunPhaseRunning,
	} {
		t.Run("Forbid"+string(phase), func(t *testing.T) {
			ctx := context.Background()
			clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
			name := "scheduled-forbid-" + strings.ToLower(string(phase))
			agent := createScheduledAgent(ctx, t, name, "* * * * *", nil)
			reconciler := newScheduledReconciler(clock)
			reconcileScheduled(ctx, t, reconciler, agent)
			createScheduledChild(ctx, t, agent, "active-"+name, "2026-07-20T11:59:00Z", 1, phase)

			clock.Set(time.Date(2026, time.July, 20, 12, 1, 5, 0, time.UTC))
			reconcileScheduled(ctx, t, reconciler, agent)
			if runs := listOwnedRuns(ctx, t, agent); len(runs) != 1 {
				t.Fatalf("Forbid created overlapping run; got %d", len(runs))
			}
			updated := getAgent(ctx, t, agent)
			assertCondition(
				t,
				updated.Status.Conditions,
				conditions.Progressing,
				metav1.ConditionFalse,
				"OverlapSkipped",
				time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC),
			)
		})
	}

	t.Run("AllowLongRunningOverlap", func(t *testing.T) {
		ctx := context.Background()
		clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
		agent := createScheduledAgent(ctx, t, "scheduled-allow", "* * * * *", &kontextv1alpha1.ScheduleSpec{
			Expression:        "* * * * *",
			ConcurrencyPolicy: kontextv1alpha1.ConcurrencyPolicyAllow,
		})
		reconciler := newScheduledReconciler(clock)
		reconcileScheduled(ctx, t, reconciler, agent)
		createScheduledChild(
			ctx,
			t,
			agent,
			"active-scheduled-allow",
			"2026-07-20T11:59:00Z",
			1,
			kontextv1alpha1.AgentRunPhaseRunning,
		)

		clock.Set(time.Date(2026, time.July, 20, 12, 1, 5, 0, time.UTC))
		reconcileScheduled(ctx, t, reconciler, agent)
		clock.Set(time.Date(2026, time.July, 20, 12, 2, 5, 0, time.UTC))
		reconcileScheduled(ctx, t, reconciler, agent)
		if runs := listOwnedRuns(ctx, t, agent); len(runs) != 3 {
			t.Fatalf("Allow did not create both overlaps; got %d runs", len(runs))
		}
		if updated := getAgent(ctx, t, agent); updated.Status.RunsCreated != 3 {
			t.Fatalf("Allow runsCreated = %d, want monotonic count 3", updated.Status.RunsCreated)
		}
	})
}

func TestScheduledSuspendAndResumeWaitsForFutureSlot(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
	agent := createScheduledAgent(ctx, t, "scheduled-suspend", "* * * * *", &kontextv1alpha1.ScheduleSpec{
		Expression: "* * * * *",
		Suspend:    true,
	})
	reconciler := newScheduledReconciler(clock)
	if result := reconcileScheduled(ctx, t, reconciler, agent); result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("suspended schedule requeued: %#v", result)
	}

	clock.Set(time.Date(2026, time.July, 20, 12, 2, 10, 0, time.UTC))
	updated := getAgent(ctx, t, agent)
	updated.Spec.Schedule.Suspend = false
	if err := k8sClient.Update(ctx, updated); err != nil {
		t.Fatalf("resume schedule: %v", err)
	}
	reconcileScheduled(ctx, t, reconciler, agent)
	if runs := listOwnedRuns(ctx, t, agent); len(runs) != 0 {
		t.Fatalf("resume backfilled %d runs", len(runs))
	}
	updated = getAgent(ctx, t, agent)
	assertTime(t, updated.Status.NextScheduleTime, time.Date(2026, time.July, 20, 12, 3, 0, 0, time.UTC))

	clock.Set(time.Date(2026, time.July, 20, 12, 3, 1, 0, time.UTC))
	reconcileScheduled(ctx, t, reconciler, agent)
	if runs := listOwnedRuns(ctx, t, agent); len(runs) != 1 {
		t.Fatalf("resumed schedule created %d runs", len(runs))
	}
}

func TestScheduledRecoversCreateSuccessStatusFailure(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
	agent := createScheduledAgent(ctx, t, "scheduled-status-retry", "* * * * *", nil)
	reconciler := newScheduledReconciler(clock)
	reconcileScheduled(ctx, t, reconciler, agent)

	clock.Set(time.Date(2026, time.July, 20, 12, 1, 5, 0, time.UTC))
	failingClient := &failAgentStatusPatchOnceClient{Client: newOwnerIndexedClient(), fail: true}
	reconciler.Client = failingClient
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(agent)}); err == nil {
		t.Fatal("expected injected status failure")
	}
	reconciler.Client = newOwnerIndexedClient()
	reconcileScheduled(ctx, t, reconciler, agent)

	runs := listOwnedRuns(ctx, t, agent)
	if len(runs) != 1 {
		t.Fatalf("status retry created %d runs", len(runs))
	}
	updated := getAgent(ctx, t, agent)
	if updated.Status.RunsCreated != 1 || updated.Status.LastRunName != runs[0].Name {
		t.Fatalf("status retry was not recovered: %#v", updated.Status)
	}
}

func TestScheduledStaleCacheAndUnrelatedCollision(t *testing.T) {
	t.Run("StaleOwnedRun", func(t *testing.T) {
		ctx := context.Background()
		clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
		agent := createScheduledAgent(ctx, t, "scheduled-stale", "* * * * *", nil)
		reconciler := newScheduledReconciler(clock)
		reconcileScheduled(ctx, t, reconciler, agent)
		slot := time.Date(2026, time.July, 20, 12, 1, 0, 0, time.UTC)
		runName := scheduler.RunName(agent.Name, slot)
		createScheduledChild(ctx, t, agent, runName, slot.Format(time.RFC3339), 1, kontextv1alpha1.AgentRunPhasePending)

		clock.Set(time.Date(2026, time.July, 20, 12, 1, 5, 0, time.UTC))
		reconciler.Client = &staleAgentRunListClient{
			Client:   newOwnerIndexedClient(),
			omitName: types.NamespacedName{Name: runName, Namespace: agent.Namespace},
		}
		reconcileScheduled(ctx, t, reconciler, agent)
		if runs := listOwnedRuns(ctx, t, agent); len(runs) != 1 {
			t.Fatalf("stale cache recovery left %d runs", len(runs))
		}
		updated := getAgent(ctx, t, agent)
		if updated.Status.RunsCreated != 1 || updated.Status.LastRunName != runName {
			t.Fatalf("stale cache status not recovered: %#v", updated.Status)
		}
	})

	t.Run("UnrelatedCollision", func(t *testing.T) {
		ctx := context.Background()
		clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
		agent := createScheduledAgent(ctx, t, "scheduled-collision", "* * * * *", nil)
		reconciler := newScheduledReconciler(clock)
		reconcileScheduled(ctx, t, reconciler, agent)
		slot := time.Date(2026, time.July, 20, 12, 1, 0, 0, time.UTC)
		unrelated := &kontextv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: scheduler.RunName(agent.Name, slot), Namespace: agent.Namespace},
			Spec: kontextv1alpha1.AgentRunSpec{
				Goal: "unrelated", Model: "echo-model", Runtime: echoRuntimeSpec(),
			},
		}
		if err := k8sClient.Create(ctx, unrelated); err != nil {
			t.Fatalf("create collision: %v", err)
		}
		clock.Set(time.Date(2026, time.July, 20, 12, 1, 5, 0, time.UTC))
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(agent)})
		if err == nil || !strings.Contains(err.Error(), "scheduled run name collision") {
			t.Fatalf("expected explicit collision, got %v", err)
		}
	})
}

func TestScheduledHistoryPruningAndActivePreservation(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 10, 0, time.UTC)}
	one := int32(1)
	agent := createScheduledAgent(ctx, t, "scheduled-prune", "0 * * * *", &kontextv1alpha1.ScheduleSpec{
		Expression:                 "0 * * * *",
		Suspend:                    true,
		SuccessfulRunsHistoryLimit: &one,
		FailedRunsHistoryLimit:     &one,
	})
	createScheduledChild(ctx, t, agent, "prune-success-old", "2026-07-20T08:00:00Z", 1, kontextv1alpha1.AgentRunPhaseSucceeded)
	createScheduledChild(ctx, t, agent, "prune-success-new", "2026-07-20T09:00:00Z", 2, kontextv1alpha1.AgentRunPhaseSucceeded)
	createScheduledChild(ctx, t, agent, "prune-failed-old", "2026-07-20T10:00:00Z", 3, kontextv1alpha1.AgentRunPhaseFailed)
	createScheduledChild(ctx, t, agent, "prune-failed-new", "2026-07-20T11:00:00Z", 4, kontextv1alpha1.AgentRunPhaseBudgetExceeded)
	createScheduledChild(ctx, t, agent, "prune-active", "2026-07-20T11:30:00Z", 5, kontextv1alpha1.AgentRunPhaseRunning)

	reconcileScheduled(ctx, t, newScheduledReconciler(clock), agent)
	assertRunExists(t, ctx, agent.Namespace, "prune-success-new", true)
	assertRunExists(t, ctx, agent.Namespace, "prune-failed-new", true)
	assertRunExists(t, ctx, agent.Namespace, "prune-active", true)
	assertRunExists(t, ctx, agent.Namespace, "prune-success-old", false)
	assertRunExists(t, ctx, agent.Namespace, "prune-failed-old", false)
	if updated := getAgent(ctx, t, agent); updated.Status.RunsCreated != 5 {
		t.Fatalf("history pruning reduced runsCreated: %#v", updated.Status)
	}

	updated := getAgent(ctx, t, agent)
	zero := int32(0)
	updated.Spec.Schedule.SuccessfulRunsHistoryLimit = &zero
	updated.Spec.Schedule.FailedRunsHistoryLimit = &zero
	if err := k8sClient.Update(ctx, updated); err != nil {
		t.Fatalf("set zero history: %v", err)
	}
	reconcileScheduled(ctx, t, newScheduledReconciler(clock), agent)
	assertRunExists(t, ctx, agent.Namespace, "prune-success-new", false)
	assertRunExists(t, ctx, agent.Namespace, "prune-failed-new", false)
	assertRunExists(t, ctx, agent.Namespace, "prune-active", true)
	updated = getAgent(ctx, t, agent)
	if updated.Status.LastRunName != "prune-active" {
		t.Fatalf("lastRunName = %q, want newest retained active run", updated.Status.LastRunName)
	}
	assertLastRunReferencesExisting(t, ctx, updated)
}

func TestScheduledHistoryPruningSelfCorrectsLastRunReference(t *testing.T) {
	t.Run("AllTerminalRunsPruned", func(t *testing.T) {
		ctx := context.Background()
		clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 30, 0, 0, time.UTC)}
		zero := int32(0)
		agent := createScheduledAgent(ctx, t, "scheduled-prune-all", "0 * * * *", &kontextv1alpha1.ScheduleSpec{
			Expression:                 "0 * * * *",
			ConcurrencyPolicy:          kontextv1alpha1.ConcurrencyPolicyAllow,
			Suspend:                    true,
			SuccessfulRunsHistoryLimit: &zero,
			FailedRunsHistoryLimit:     &zero,
		})
		slot := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
		createScheduledChild(
			ctx,
			t,
			agent,
			"prune-all-terminal",
			slot.Format(time.RFC3339),
			1,
			kontextv1alpha1.AgentRunPhaseSucceeded,
		)
		seedScheduledStatus(ctx, t, agent, "prune-all-terminal", slot, 1)

		reconciler := newScheduledReconciler(clock)
		for range 20 {
			reconcileScheduled(ctx, t, reconciler, agent)
			updated := getAgent(ctx, t, agent)
			if updated.Status.LastRunName != "" {
				t.Fatalf("all-pruned lastRunName stayed stale: %#v", updated.Status)
			}
			assertTime(t, updated.Status.LastScheduleTime, slot)
			if updated.Status.RunsCreated != 1 {
				t.Fatalf("all-pruned runsCreated regressed: %#v", updated.Status)
			}
		}
		assertRunExists(t, ctx, agent.Namespace, "prune-all-terminal", false)
	})

	t.Run("NewerTerminalPrunedOlderActiveRetained", func(t *testing.T) {
		ctx := context.Background()
		clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 30, 0, 0, time.UTC)}
		zero := int32(0)
		agent := createScheduledAgent(ctx, t, "scheduled-prune-mixed", "0 * * * *", &kontextv1alpha1.ScheduleSpec{
			Expression:                 "0 * * * *",
			ConcurrencyPolicy:          kontextv1alpha1.ConcurrencyPolicyAllow,
			Suspend:                    true,
			SuccessfulRunsHistoryLimit: &zero,
			FailedRunsHistoryLimit:     &zero,
		})
		activeSlot := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
		terminalSlot := time.Date(2026, time.July, 20, 11, 0, 0, 0, time.UTC)
		createScheduledChild(
			ctx,
			t,
			agent,
			"prune-mixed-active",
			activeSlot.Format(time.RFC3339),
			1,
			kontextv1alpha1.AgentRunPhaseRunning,
		)
		createScheduledChild(
			ctx,
			t,
			agent,
			"prune-mixed-terminal",
			terminalSlot.Format(time.RFC3339),
			2,
			kontextv1alpha1.AgentRunPhaseSucceeded,
		)
		seedScheduledStatus(ctx, t, agent, "prune-mixed-terminal", terminalSlot, 2)

		reconciler := newScheduledReconciler(clock)
		for range 20 {
			reconcileScheduled(ctx, t, reconciler, agent)
			updated := getAgent(ctx, t, agent)
			if updated.Status.LastRunName != "prune-mixed-active" {
				t.Fatalf("mixed lastRunName did not self-correct: %#v", updated.Status)
			}
			assertTime(t, updated.Status.LastScheduleTime, terminalSlot)
			if updated.Status.RunsCreated != 2 {
				t.Fatalf("mixed runsCreated regressed: %#v", updated.Status)
			}
			assertLastRunReferencesExisting(t, ctx, updated)
		}
		assertRunExists(t, ctx, agent.Namespace, "prune-mixed-terminal", false)
		assertRunExists(t, ctx, agent.Namespace, "prune-mixed-active", true)
	})
}

func TestScheduledDefaultsModeValidationAndInvalidPolicyCondition(t *testing.T) {
	ctx := context.Background()
	agent := createScheduledAgent(ctx, t, "scheduled-defaults", "* * * * *", nil)
	stored := getAgent(ctx, t, agent)
	if stored.Spec.Schedule.TimeZone != "Etc/UTC" ||
		stored.Spec.Schedule.ConcurrencyPolicy != kontextv1alpha1.ConcurrencyPolicyForbid ||
		stored.Spec.Schedule.StartingDeadlineSeconds == nil ||
		*stored.Spec.Schedule.StartingDeadlineSeconds != 60 ||
		stored.Spec.Schedule.SuccessfulRunsHistoryLimit == nil ||
		*stored.Spec.Schedule.SuccessfulRunsHistoryLimit != 3 ||
		stored.Spec.Schedule.FailedRunsHistoryLimit == nil ||
		*stored.Spec.Schedule.FailedRunsHistoryLimit != 1 {
		t.Fatalf("API defaults missing: %#v", stored.Spec.Schedule)
	}

	invalidMode := newScheduledAgent("scheduled-missing-policy", "* * * * *", nil)
	invalidMode.Spec.Schedule = nil
	if err := k8sClient.Create(ctx, invalidMode); err == nil {
		t.Fatal("Scheduled Agent without schedule was admitted")
	}
	serviceWithSchedule := newScheduledAgent("service-with-schedule", "* * * * *", nil)
	serviceWithSchedule.Spec.Mode = kontextv1alpha1.AgentModeService
	if err := k8sClient.Create(ctx, serviceWithSchedule); err == nil {
		t.Fatal("Service Agent with schedule was admitted")
	}
	scheduledWithBackoff := newScheduledAgent("scheduled-with-backoff", "* * * * *", nil)
	scheduledWithBackoff.Spec.Backoff = &kontextv1alpha1.BackoffSpec{InitialSeconds: 1, MaxSeconds: 2}
	if err := k8sClient.Create(ctx, scheduledWithBackoff); err == nil {
		t.Fatal("Scheduled Agent with Service backoff was admitted")
	}
	serviceWithoutGoal := newScheduledAgent("service-without-goal", "* * * * *", nil)
	serviceWithoutGoal.Spec.Mode = kontextv1alpha1.AgentModeService
	serviceWithoutGoal.Spec.Schedule = nil
	serviceWithoutGoal.Spec.Goal = ""
	if err := k8sClient.Create(ctx, serviceWithoutGoal); err == nil {
		t.Fatal("Service Agent without a concrete goal was admitted")
	}
	for _, test := range []struct {
		name       string
		expression string
	}{
		{name: "six-fields", expression: "0 0 0 1 1 1"},
		{name: "space-leading-cron-tz", expression: "CRON_TZ=UTC 0 * * * *"},
		{name: "tab-leading-tz", expression: "\tTZ=UTC * * * *"},
		{name: "tab-embedded-tz", expression: "*\tTZ=UTC * * *"},
		{name: "newline-leading-cron-tz", expression: "\nCRON_TZ=UTC * * * *"},
		{name: "newline-embedded-cron-tz", expression: "*\nCRON_TZ=UTC * * *"},
	} {
		invalid := newScheduledAgent(
			"scheduled-invalid-"+test.name,
			test.expression,
			nil,
		)
		if err := k8sClient.Create(ctx, invalid); err == nil {
			t.Fatalf("invalid expression %q was admitted", test.expression)
		}
	}
	for _, test := range []struct {
		name       string
		expression string
	}{
		{name: "tabs", expression: "*\t*\t*\t*\t*"},
		{name: "newlines", expression: "*\n*\n*\n*\n*"},
	} {
		valid := newScheduledAgent("scheduled-valid-whitespace-"+test.name, test.expression, nil)
		if err := k8sClient.Create(ctx, valid); err != nil {
			t.Fatalf("valid whitespace expression %q was rejected: %v", test.expression, err)
		}
	}

	invalidZone := newScheduledAgent("scheduled-invalid-zone", "* * * * *", &kontextv1alpha1.ScheduleSpec{
		Expression: "* * * * *",
		TimeZone:   "Mars/Olympus_Mons",
	})
	if err := k8sClient.Create(ctx, invalidZone); err != nil {
		t.Fatalf("create invalid-zone Agent for controller validation: %v", err)
	}
	clock := &fakeClock{now: time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)}
	reconcileScheduled(ctx, t, newScheduledReconciler(clock), invalidZone)
	updated := getAgent(ctx, t, invalidZone)
	assertCondition(t, updated.Status.Conditions, conditions.Ready, metav1.ConditionFalse, "InvalidSchedule", clock.Now())
}

type failAgentStatusPatchOnceClient struct {
	client.Client
	fail bool
}

func (c *failAgentStatusPatchOnceClient) Status() client.SubResourceWriter {
	return &failAgentStatusWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type failAgentStatusWriter struct {
	client.SubResourceWriter
	parent *failAgentStatusPatchOnceClient
}

func (w *failAgentStatusWriter) Patch(
	ctx context.Context,
	obj client.Object,
	patch client.Patch,
	opts ...client.SubResourcePatchOption,
) error {
	if _, ok := obj.(*kontextv1alpha1.Agent); ok && w.parent.fail {
		w.parent.fail = false
		return errors.New("injected Agent status patch failure")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func newScheduledReconciler(clock scheduler.Clock) *controller.AgentReconciler {
	return &controller.AgentReconciler{
		Client:    newOwnerIndexedClient(),
		APIReader: apiReader,
		Scheme:    scheme,
		Clock:     clock,
	}
}

func newScheduledAgent(
	name string,
	expression string,
	override *kontextv1alpha1.ScheduleSpec,
) *kontextv1alpha1.Agent {
	schedule := override
	if schedule == nil {
		schedule = &kontextv1alpha1.ScheduleSpec{Expression: expression}
	}
	return &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:     kontextv1alpha1.AgentModeScheduled,
			Goal:     "run scheduled work",
			Provider: "echo",
			Model:    "echo-model",
			Runtime:  echoRuntimeSpec(),
			Schedule: schedule,
		},
	}
}

func createScheduledAgent(
	ctx context.Context,
	t *testing.T,
	name string,
	expression string,
	override *kontextv1alpha1.ScheduleSpec,
) *kontextv1alpha1.Agent {
	t.Helper()
	agent := newScheduledAgent(name, expression, override)
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create Scheduled Agent: %v", err)
	}
	return agent
}

func reconcileScheduled(
	ctx context.Context,
	t *testing.T,
	reconciler *controller.AgentReconciler,
	agent *kontextv1alpha1.Agent,
) ctrl.Result {
	t.Helper()
	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(agent)})
	if err != nil {
		t.Fatalf("reconcile Scheduled Agent: %v", err)
	}
	return result
}

func getAgent(ctx context.Context, t *testing.T, agent *kontextv1alpha1.Agent) *kontextv1alpha1.Agent {
	t.Helper()
	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(agent), &updated); err != nil {
		t.Fatalf("get Agent: %v", err)
	}
	return &updated
}

func seedScheduledStatus(
	ctx context.Context,
	t *testing.T,
	agent *kontextv1alpha1.Agent,
	lastRunName string,
	lastScheduleTime time.Time,
	runsCreated int32,
) {
	t.Helper()
	updated := getAgent(ctx, t, agent)
	if err := updateAgentStatus(ctx, updated, kontextv1alpha1.AgentStatus{
		LastRunName:        lastRunName,
		LastScheduleTime:   timePtrForTest(lastScheduleTime),
		RunsCreated:        runsCreated,
		ObservedGeneration: updated.Generation,
	}); err != nil {
		t.Fatalf("seed Scheduled Agent status: %v", err)
	}
}

func assertLastRunReferencesExisting(
	t *testing.T,
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
) {
	t.Helper()
	if agent.Status.LastRunName == "" {
		return
	}
	assertRunExists(t, ctx, agent.Namespace, agent.Status.LastRunName, true)
}

func timePtrForTest(value time.Time) *metav1.Time {
	result := metav1.NewTime(value.UTC())
	return &result
}

func createScheduledChild(
	ctx context.Context,
	t *testing.T,
	agent *kontextv1alpha1.Agent,
	name string,
	slot string,
	sequence int32,
	phase kontextv1alpha1.AgentRunPhase,
) {
	t.Helper()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
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
	scheduledSlot, err := time.Parse(time.RFC3339, slot)
	if err != nil {
		t.Fatalf("parse scheduled child slot: %v", err)
	}
	scheduledrun.SetMetadata(run, scheduledSlot, sequence)
	assertScheduledMetadata(t, run, scheduledSlot, sequence)
	createOwnedAgentRun(ctx, t, agent, run)
	if err := updateAgentRunStatus(ctx, run, kontextv1alpha1.AgentRunStatus{
		Phase: phase,
	}); err != nil {
		t.Fatalf("update scheduled child status: %v", err)
	}
}

func assertScheduledMetadata(
	t *testing.T,
	run *kontextv1alpha1.AgentRun,
	wantSlot time.Time,
	wantSequence int32,
) {
	t.Helper()
	slot, slotOK := scheduledrun.Slot(run)
	sequence, sequenceOK := scheduledrun.Sequence(run)
	if !slotOK || !slot.Equal(wantSlot) || !sequenceOK || sequence != wantSequence {
		t.Fatalf(
			"scheduled metadata guard failed: slot=%s/%t sequence=%d/%t annotations=%#v",
			slot,
			slotOK,
			sequence,
			sequenceOK,
			run.Annotations,
		)
	}
}

func listOwnedRuns(
	ctx context.Context,
	t *testing.T,
	agent *kontextv1alpha1.Agent,
) []kontextv1alpha1.AgentRun {
	t.Helper()
	var list kontextv1alpha1.AgentRunList
	if err := k8sClient.List(
		ctx,
		&list,
		client.InNamespace(agent.Namespace),
		client.MatchingLabels{podbuilder.LabelAgentName: agent.Name},
	); err != nil {
		t.Fatalf("list scheduled children: %v", err)
	}
	result := make([]kontextv1alpha1.AgentRun, 0, len(list.Items))
	for i := range list.Items {
		if metav1.IsControlledBy(&list.Items[i], agent) {
			result = append(result, list.Items[i])
		}
	}
	return result
}

func assertTime(t *testing.T, got *metav1.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Time.Equal(want) {
		t.Fatalf("time = %v, want %s", got, want)
	}
}

func assertCondition(
	t *testing.T,
	all []metav1.Condition,
	conditionType string,
	status metav1.ConditionStatus,
	reason string,
	transition time.Time,
) {
	t.Helper()
	for i := range all {
		condition := all[i]
		if condition.Type != conditionType {
			continue
		}
		if condition.Status != status || condition.Reason != reason {
			t.Fatalf("condition %s = %#v", conditionType, condition)
		}
		if !condition.LastTransitionTime.Time.Equal(transition) {
			t.Fatalf(
				"condition %s transition = %s, want %s",
				conditionType,
				condition.LastTransitionTime,
				transition,
			)
		}
		return
	}
	t.Fatalf("condition %s not found in %#v", conditionType, all)
}

func assertRunExists(t *testing.T, ctx context.Context, namespace string, name string, want bool) {
	t.Helper()
	var run kontextv1alpha1.AgentRun
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &run)
	if want && err != nil {
		t.Fatalf("expected AgentRun %s: %v", name, err)
	}
	if !want && !apierrors.IsNotFound(err) {
		t.Fatalf("expected AgentRun %s to be absent, got %v", name, err)
	}
}
