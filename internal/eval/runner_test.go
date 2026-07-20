package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

func TestRunnerCollectsGradesJudgesThenCleansOnlyCreatedRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	cluster := &terminalClient{Client: base}
	logs := successfulLogs(t)
	judge := &orderingJudge{t: t}
	timeout := Duration{Duration: time.Second}
	suite := EvalSuite{
		Metadata: Metadata{Name: "suite"},
		Spec: SuiteSpec{
			Defaults: SuiteDefaults{Namespace: "evals", Timeout: &timeout},
			Cases: []Case{{
				ID:      "case",
				Timeout: &timeout,
				AgentRun: kontextv1alpha1.AgentRunSpec{
					Goal: "goal", Model: "model", Runtime: kontextv1alpha1.RuntimeSpec{Image: "runtime"},
				},
				Graders: []Grader{
					{Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseSucceeded},
					{Type: GraderEnvelopeOutcome, Outcome: resultv1alpha1.OutcomeSucceeded},
					{Type: GraderEventCount, Event: &EventCountExpectation{Type: eventv1alpha1.TypeLifecycle, Count: 1}},
					{Type: GraderPodExitCode, ExitCode: int32Pointer(0)},
				},
			}},
		},
	}
	runner := Runner{
		Client: cluster,
		Logs:   &staticLogs{data: logs},
		Options: RunnerOptions{
			PollInterval: time.Millisecond,
			InvocationID: "invocation",
			Judge:        judge,
		},
	}
	records := runner.RunSuite(context.Background(), suite)
	if len(records) != 1 || !records[0].Pass {
		t.Fatalf("unexpected records %#v", records)
	}
	if records[0].Envelope == nil || records[0].Envelope.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("termination message envelope was not collected: %#v", records[0].Envelope)
	}
	encodedRecord, err := json.Marshal(records[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private output", "secret-extension", "request-secret", "artifact-secret"} {
		if bytes.Contains(encodedRecord, []byte(forbidden)) {
			t.Fatalf("projected envelope leaked %q: %s", forbidden, encodedRecord)
		}
	}
	if !judge.called || cluster.deletes != 1 {
		t.Fatalf("judge/cleanup ordering not exercised: judge=%v deletes=%d", judge.called, cluster.deletes)
	}
	remaining := &kontextv1alpha1.AgentRun{}
	err = base.Get(context.Background(), types.NamespacedName{
		Namespace: "evals", Name: records[0].Run.Name,
	}, remaining)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("created run was not cleaned up: %v", err)
	}
}

func TestRunnerKeepRunsAndCreateFailureCleanup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kontextv1alpha1.AddToScheme(scheme)
	timeout := Duration{Duration: 10 * time.Millisecond}
	suite := EvalSuite{
		Metadata: Metadata{Name: "suite"},
		Spec: SuiteSpec{
			Defaults: SuiteDefaults{Namespace: "evals", Timeout: &timeout},
			Cases: []Case{{
				ID: "case", Timeout: &timeout,
				AgentRun: kontextv1alpha1.AgentRunSpec{
					Goal: "goal", Model: "model", Runtime: kontextv1alpha1.RuntimeSpec{Image: "runtime"},
				},
			}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	cluster := &terminalClient{Client: base}
	runner := Runner{
		Client: cluster, Logs: &staticLogs{data: successfulLogs(t)},
		Options: RunnerOptions{KeepRuns: true, PollInterval: time.Millisecond, InvocationID: "keep"},
	}
	records := runner.RunSuite(context.Background(), suite)
	if len(records) != 1 || cluster.deletes != 0 {
		t.Fatalf("keep-runs deleted resources: %#v deletes=%d", records, cluster.deletes)
	}

	failing := &createFailClient{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	runner.Client = failing
	runner.Options.KeepRuns = false
	records = runner.RunSuite(context.Background(), suite)
	if len(records) != 1 || records[0].Pass || failing.deletes != 0 {
		t.Fatalf("create failure cleanup was unsafe: %#v deletes=%d", records, failing.deletes)
	}
}

func TestAmbiguousCreateCleansOnlyExactlyOwnedRun(t *testing.T) {
	timeout := Duration{Duration: time.Second}
	suite := EvalSuite{
		Metadata: Metadata{Name: "ambiguous"},
		Spec: SuiteSpec{
			Defaults: SuiteDefaults{Namespace: "evals", Timeout: &timeout},
			Cases: []Case{{
				ID: "case", Timeout: &timeout,
				AgentRun: kontextv1alpha1.AgentRunSpec{
					Goal: "goal", Model: "model", Runtime: kontextv1alpha1.RuntimeSpec{Image: "runtime"},
				},
				Graders: []Grader{{
					Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseSucceeded,
				}},
			}},
		},
	}
	for name, unowned := range map[string]bool{"owned": false, "unowned": true} {
		t.Run(name, func(t *testing.T) {
			base := newFakeEvalClient(t)
			cluster := &ambiguousCreateClient{
				Client:             base,
				unowned:            unowned,
				probeNotFoundCount: 1,
			}
			records := (Runner{
				Client: cluster,
				Options: RunnerOptions{
					PollInterval: time.Millisecond,
					InvocationID: name,
				},
			}).RunSuite(context.Background(), suite)
			if len(records) != 1 || records[0].Pass {
				t.Fatalf("ambiguous create unexpectedly passed: %#v", records)
			}
			key := types.NamespacedName{
				Namespace: records[0].Run.Namespace,
				Name:      records[0].Run.Name,
			}
			remaining := &kontextv1alpha1.AgentRun{}
			err := base.Get(context.Background(), key, remaining)
			if unowned {
				if err != nil || cluster.deletes != 0 {
					t.Fatalf("unowned collision was modified: err=%v deletes=%d", err, cluster.deletes)
				}
			} else {
				if !apierrors.IsNotFound(err) || cluster.deletes != 1 {
					t.Fatalf("owned ambiguous run was not cleaned: err=%v deletes=%d", err, cluster.deletes)
				}
			}
		})
	}
}

func TestRunnerDoesNotFetchUnrequiredArtifactsOrStoreOutput(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kontextv1alpha1.AddToScheme(scheme)
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	cluster := &terminalClient{Client: base}
	logs := &staticLogs{data: []byte("sensitive logs")}
	timeout := Duration{Duration: time.Second}
	suite := EvalSuite{
		Metadata: Metadata{Name: "suite"},
		Spec: SuiteSpec{
			Defaults: SuiteDefaults{Namespace: "evals", Timeout: &timeout},
			Cases: []Case{{
				ID: "status-only", Timeout: &timeout,
				AgentRun: kontextv1alpha1.AgentRunSpec{
					Goal: "goal", Model: "model", Runtime: kontextv1alpha1.RuntimeSpec{Image: "runtime"},
				},
				Graders: []Grader{{
					Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseSucceeded,
				}},
			}},
		},
	}
	records := (Runner{
		Client: cluster,
		Logs:   logs,
		Options: RunnerOptions{
			PollInterval: time.Millisecond,
			InvocationID: "status-only",
		},
	}).RunSuite(context.Background(), suite)
	if len(records) != 1 || !records[0].Pass {
		t.Fatalf("unexpected status-only record: %#v", records)
	}
	record := records[0]
	if cluster.podGets != 0 || logs.calls != 0 {
		t.Fatalf("unrequired artifacts were fetched: podGets=%d logCalls=%d", cluster.podGets, logs.calls)
	}
	if record.StatusResult != "" || record.StatusOutput != nil || record.StatusUsage != nil ||
		record.Envelope != nil || len(record.Events.Metadata) != 0 {
		t.Fatalf("unrequested output was retained: %#v", record)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("sensitive model output")) {
		t.Fatalf("serialized record retained unrequested model output: %s", encoded)
	}
}

func TestFailureStatusMessageIsNotRecordedOrJudged(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kontextv1alpha1.AddToScheme(scheme)
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	cluster := &terminalClient{
		Client:  base,
		phase:   kontextv1alpha1.AgentRunPhaseFailed,
		message: "private failure detail",
	}
	timeout := Duration{Duration: time.Second}
	suite := EvalSuite{
		Metadata: Metadata{Name: "suite"},
		Spec: SuiteSpec{
			Defaults: SuiteDefaults{Namespace: "evals", Timeout: &timeout},
			Cases: []Case{{
				ID: "failure", Timeout: &timeout,
				AgentRun: kontextv1alpha1.AgentRunSpec{
					Goal: "goal", Model: "model", Runtime: kontextv1alpha1.RuntimeSpec{Image: "runtime"},
				},
				Graders: []Grader{{
					Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseFailed,
				}},
			}},
		},
	}
	records := (Runner{
		Client: cluster,
		Options: RunnerOptions{
			PollInterval: time.Millisecond,
			InvocationID: "failure",
		},
	}).RunSuite(context.Background(), suite)
	if len(records) != 1 || !records[0].Pass {
		t.Fatalf("unexpected failure record: %#v", records)
	}
	encoded, err := json.Marshal(records[0])
	if err != nil {
		t.Fatal(err)
	}
	observation, err := json.Marshal(observationFor(records[0]))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private failure detail")) ||
		bytes.Contains(observation, []byte("private failure detail")) ||
		bytes.Contains(observation, []byte("terminalMessage")) {
		t.Fatalf("status.message leaked into record or judge: record=%s judge=%s", encoded, observation)
	}
}

func TestEnvelopeGradersUseTerminationMessageWithoutLogs(t *testing.T) {
	logs := &staticLogs{data: []byte("KONTEXT_RESULT: {not-authoritative}\n")}
	pod := &corev1.Pod{}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "runtime",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 0,
			Message:  successfulEnvelopeMessage(),
		}},
	}}
	record := Record{StartedAt: time.Unix(0, 0)}
	turns := int32(2)
	tools := int32(1)
	item := Case{Graders: []Grader{
		{Type: GraderEnvelopeOutcome, Outcome: resultv1alpha1.OutcomeSucceeded},
		{Type: GraderExecutionModel, Model: "model"},
		{Type: GraderEnvelopeTurns, Turns: &turns},
		{Type: GraderEnvelopeTools, ToolCalls: &tools},
	}}
	requirements := mustArtifactRequirements(t, item.Graders)
	finished := (Runner{Logs: logs}).finishRecord(
		context.Background(),
		&record,
		item,
		requirements,
		nil,
		pod,
		false,
		func() time.Time { return time.Unix(1, 0) },
	)
	if !finished.Pass || finished.Envelope == nil {
		t.Fatalf("termination-message envelope did not pass: %#v", finished)
	}
	if finished.Envelope.Execution == nil ||
		finished.Envelope.Execution.Model != "model" ||
		finished.Envelope.Execution.Turns == nil ||
		*finished.Envelope.Execution.Turns != turns ||
		finished.Envelope.Execution.ToolCalls == nil ||
		*finished.Envelope.Execution.ToolCalls != tools {
		t.Fatalf("requested envelope projection is incomplete: %#v", finished.Envelope)
	}
	if logs.calls != 0 {
		t.Fatalf("envelope-only grading fetched logs %d times", logs.calls)
	}
}

func TestEventGradersFailTransportAndTruncationErrors(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Namespace = "evals"
	pod.Name = "runtime"
	item := Case{Graders: []Grader{{
		Type: GraderEventCount,
		Event: &EventCountExpectation{
			Type: eventv1alpha1.TypeTool, Count: 0,
		},
	}}}
	for name, logs := range map[string]*staticLogs{
		"transport":  {err: errors.New("stream failed")},
		"truncation": {truncated: true},
	} {
		t.Run(name, func(t *testing.T) {
			record := Record{StartedAt: time.Unix(0, 0)}
			judge := &orderingJudge{t: t}
			requirements := mustArtifactRequirements(t, item.Graders)
			finished := (Runner{Logs: logs, Options: RunnerOptions{Judge: judge}}).finishRecord(
				context.Background(),
				&record,
				item,
				requirements,
				nil,
				pod,
				false,
				func() time.Time { return time.Unix(1, 0) },
			)
			if finished.Pass || len(finished.CollectionErrors) == 0 {
				t.Fatalf("incomplete event collection passed: %#v", finished)
			}
			if !judge.called || finished.Judge == nil || !finished.Judge.Pass {
				t.Fatalf("judge did not run after deterministic grading: %#v", finished.Judge)
			}
		})
	}
}

func TestCleanupUsesFreshContextAfterCancellation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kontextv1alpha1.AddToScheme(scheme)
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	run := &kontextv1alpha1.AgentRun{}
	run.Namespace = "evals"
	run.Name = "created-run"
	if err := base.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	cluster := &cleanupContextClient{Client: base}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	record := Record{
		Run:           RunRef{Namespace: run.Namespace, Name: run.Name},
		StartedAt:     time.Unix(0, 0),
		TerminalPhase: kontextv1alpha1.AgentRunPhaseSucceeded,
	}
	item := Case{Graders: []Grader{{
		Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseSucceeded,
	}}}
	requirements := mustArtifactRequirements(t, item.Graders)
	finished := (Runner{Client: cluster}).finishRecord(
		ctx,
		&record,
		item,
		requirements,
		run,
		nil,
		true,
		func() time.Time { return time.Unix(1, 0) },
	)
	if cluster.deletes != 1 || cluster.deleteContextErr != nil || !cluster.hadDeadline {
		t.Fatalf(
			"cleanup context was not fresh and bounded: deletes=%d err=%v deadline=%v",
			cluster.deletes,
			cluster.deleteContextErr,
			cluster.hadDeadline,
		)
	}
	if finished.Pass ||
		containsCollectionError(finished.CollectionErrors, "cleanup AgentRun") {
		t.Fatalf("canceled case cleanup result was incorrect: %#v", finished)
	}
}

func TestBoundedTailRetainsNewestBytes(t *testing.T) {
	tail := newBoundedTail(5)
	if _, err := tail.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := tail.Write([]byte("defg")); err != nil {
		t.Fatal(err)
	}
	if got := string(tail.Bytes()); got != "cdefg" {
		t.Fatalf("expected bounded tail, got %q", got)
	}
	if !tail.Truncated() {
		t.Fatal("expected truncation to be reported")
	}
}

func TestKubernetesLogFetcherSetsServerBoundsAndReportsIncompleteTail(t *testing.T) {
	var captured *corev1.PodLogOptions
	fetcher := KubernetesLogFetcher{
		Stream: func(
			_ context.Context,
			_, _ string,
			options *corev1.PodLogOptions,
		) (io.ReadCloser, error) {
			copy := options.DeepCopy()
			captured = copy
			lines := strings.Repeat("event\n", int(MaxLogTailLines))
			return io.NopCloser(strings.NewReader(lines)), nil
		},
	}
	logs, err := fetcher.Fetch(context.Background(), "evals", "pod", "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if captured == nil ||
		captured.TailLines == nil ||
		*captured.TailLines != MaxLogTailLines ||
		captured.LimitBytes == nil ||
		*captured.LimitBytes != int64(MaxLogBytes+1) {
		t.Fatalf("server-side log bounds were not configured: %#v", captured)
	}
	if !logs.Truncated {
		t.Fatal("tail-line boundary was not reported as potentially incomplete")
	}
}

func TestFinishRecordAllowsStatusOnlyResultWithoutPodArtifacts(t *testing.T) {
	timeout := Duration{Duration: time.Second}
	item := Case{
		ID:      "wallclock",
		Timeout: &timeout,
		Graders: []Grader{{
			Type:  GraderTerminalPhase,
			Phase: kontextv1alpha1.AgentRunPhaseBudgetExceeded,
		}},
	}
	record := Record{
		StartedAt:     time.Unix(0, 0),
		TerminalPhase: kontextv1alpha1.AgentRunPhaseBudgetExceeded,
	}
	now := func() time.Time { return time.Unix(1, 0) }
	requirements := mustArtifactRequirements(t, item.Graders)

	finished := (Runner{}).finishRecord(
		context.Background(),
		&record,
		item,
		requirements,
		nil,
		nil,
		false,
		now,
	)
	if !finished.Pass || len(finished.CollectionErrors) != 0 {
		t.Fatalf("status-only record should not require Pod artifacts: %#v", finished)
	}
}

func TestFinishRecordFailsWhenGraderRequiredArtifactIsMissing(t *testing.T) {
	exitCode := int32(7)
	turns := int32(1)
	cases := map[string]Grader{
		"event": {
			Type:  GraderEventCount,
			Event: &EventCountExpectation{Type: eventv1alpha1.TypeTool, Count: 1},
		},
		"envelope": {
			Type:  GraderEnvelopeTurns,
			Turns: &turns,
		},
		"exit": {
			Type:     GraderPodExitCode,
			ExitCode: &exitCode,
		},
	}
	for name, grader := range cases {
		t.Run(name, func(t *testing.T) {
			requirements := mustArtifactRequirements(t, []Grader{grader})
			if !requirements.pod {
				t.Fatal("artifact grader did not require a Pod")
			}
			if name == "event" && len(requirements.eventTypes) == 0 {
				t.Fatal("event grader did not require events")
			}
			if name == "envelope" && !requirements.envelope {
				t.Fatal("envelope grader did not require an envelope")
			}

			record := Record{StartedAt: time.Unix(0, 0)}
			item := Case{Graders: []Grader{grader}}
			finished := (Runner{}).finishRecord(
				context.Background(),
				&record,
				item,
				requirements,
				nil,
				nil,
				false,
				func() time.Time { return time.Unix(1, 0) },
			)
			if finished.Pass {
				t.Fatalf("missing required artifact unexpectedly passed: %#v", finished)
			}
		})
	}
}
