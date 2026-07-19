package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	eventv1alpha1 "github.com/kontext-dev/kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
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
	finished := (Runner{Logs: logs}).finishRecord(
		context.Background(),
		&record,
		item,
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
			finished := (Runner{Logs: logs, Options: RunnerOptions{Judge: judge}}).finishRecord(
				context.Background(),
				&record,
				item,
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
	finished := (Runner{Client: cluster}).finishRecord(
		ctx,
		&record,
		item,
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

func TestRunnerRetriesTransientAgentRunAndPodGets(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kontextv1alpha1.AddToScheme(scheme)
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	terminal := &terminalClient{Client: base}
	cluster := &sequencedGetClient{
		Client: terminal,
		agentErrors: []error{
			&url.Error{Op: "GET", URL: "https://cluster", Err: io.ErrUnexpectedEOF},
			syscall.ECONNRESET,
			apierrors.NewServiceUnavailable("try again"),
		},
		podErrors: []error{apierrors.NewTooManyRequests("slow down", 0)},
	}
	timeout := Duration{Duration: time.Second}
	suite := EvalSuite{
		Metadata: Metadata{Name: "retry"},
		Spec: SuiteSpec{
			Defaults: SuiteDefaults{Namespace: "evals", Timeout: &timeout},
			Cases: []Case{{
				ID: "transient", Timeout: &timeout,
				AgentRun: kontextv1alpha1.AgentRunSpec{
					Goal: "goal", Model: "model", Runtime: kontextv1alpha1.RuntimeSpec{Image: "runtime"},
				},
				Graders: []Grader{{
					Type: GraderEnvelopeOutcome, Outcome: resultv1alpha1.OutcomeSucceeded,
				}},
			}},
		},
	}
	records := (Runner{
		Client: cluster,
		Options: RunnerOptions{
			PollInterval: time.Millisecond,
			InvocationID: "retry",
		},
	}).RunSuite(context.Background(), suite)
	if len(records) != 1 || !records[0].Pass {
		t.Fatalf("transient reads did not recover: %#v", records)
	}
	if cluster.agentGets < 4 || cluster.podGets < 2 || terminal.deletes != 1 {
		t.Fatalf(
			"transient reads were not retried before cleanup: agent=%d pod=%d deletes=%d",
			cluster.agentGets,
			cluster.podGets,
			terminal.deletes,
		)
	}
}

func TestRunnerFailsPermanentGetImmediately(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kontextv1alpha1.AddToScheme(scheme)
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	terminal := &terminalClient{Client: base}
	cluster := &sequencedGetClient{
		Client:      terminal,
		agentErrors: []error{apierrors.NewBadRequest("invalid read")},
	}
	timeout := Duration{Duration: time.Second}
	suite := EvalSuite{
		Metadata: Metadata{Name: "permanent"},
		Spec: SuiteSpec{
			Defaults: SuiteDefaults{Namespace: "evals", Timeout: &timeout},
			Cases: []Case{{
				ID: "permanent", Timeout: &timeout,
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
		Options: RunnerOptions{
			PollInterval: time.Millisecond,
			InvocationID: "permanent",
		},
	}).RunSuite(context.Background(), suite)
	if len(records) != 1 || records[0].Pass || cluster.agentGets != 1 {
		t.Fatalf("permanent read was not failed immediately: records=%#v gets=%d", records, cluster.agentGets)
	}
}

func TestTransientGetErrorClassification(t *testing.T) {
	resource := schema.GroupResource{Group: "kontext.dev", Resource: "agentruns"}
	for name, err := range map[string]error{
		"timeout":             apierrors.NewTimeoutError("timeout", 1),
		"server timeout":      apierrors.NewServerTimeout(resource, "get", 1),
		"too many requests":   apierrors.NewTooManyRequests("limited", 1),
		"service unavailable": apierrors.NewServiceUnavailable("down"),
		"internal":            apierrors.NewInternalError(errors.New("temporary")),
		"url timeout": &url.Error{
			Op: "GET", URL: "https://cluster", Err: io.ErrUnexpectedEOF,
		},
		"connection reset":   syscall.ECONNRESET,
		"connection refused": syscall.ECONNREFUSED,
	} {
		t.Run(name, func(t *testing.T) {
			if !transientGetError(err) {
				t.Fatalf("error was not classified transient: %v", err)
			}
		})
	}
	if transientGetError(apierrors.NewBadRequest("permanent")) {
		t.Fatal("permanent error was classified transient")
	}
	if transientGetError(apierrors.NewUnauthorized("permanent")) {
		t.Fatal("unauthorized error was classified transient")
	}
}

func TestCaseTimeoutCoversCreateWaitPodLogsAndJudge(t *testing.T) {
	timeout := 40 * time.Millisecond
	tests := map[string]func(*testing.T) (client.Client, LogFetcher, Judge, []Grader){
		"create": func(t *testing.T) (client.Client, LogFetcher, Judge, []Grader) {
			return &blockingCreateClient{Client: newFakeEvalClient(t)}, nil, nil, []Grader{{
				Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseSucceeded,
			}}
		},
		"wait": func(t *testing.T) (client.Client, LogFetcher, Judge, []Grader) {
			return newFakeEvalClient(t), nil, nil, []Grader{{
				Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseSucceeded,
			}}
		},
		"pod": func(t *testing.T) (client.Client, LogFetcher, Judge, []Grader) {
			terminal := &terminalClient{Client: newFakeEvalClient(t)}
			return &blockingPodGetClient{Client: terminal}, nil, nil, []Grader{{
				Type: GraderEnvelopeOutcome, Outcome: resultv1alpha1.OutcomeSucceeded,
			}}
		},
		"logs": func(t *testing.T) (client.Client, LogFetcher, Judge, []Grader) {
			return &terminalClient{Client: newFakeEvalClient(t)}, blockingLogs{}, nil, []Grader{{
				Type: GraderEventCount,
				Event: &EventCountExpectation{
					Type: eventv1alpha1.TypeTool, Count: 0,
				},
			}}
		},
		"judge": func(t *testing.T) (client.Client, LogFetcher, Judge, []Grader) {
			return &terminalClient{Client: newFakeEvalClient(t)}, nil, blockingJudge{}, []Grader{{
				Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseSucceeded,
			}}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			cluster, logs, judge, graders := setup(t)
			duration := Duration{Duration: timeout}
			suite := EvalSuite{
				Metadata: Metadata{Name: "timeout"},
				Spec: SuiteSpec{
					Defaults: SuiteDefaults{Namespace: "evals", Timeout: &duration},
					Cases: []Case{{
						ID: "case", Timeout: &duration,
						AgentRun: kontextv1alpha1.AgentRunSpec{
							Goal: "goal", Model: "model",
							Runtime: kontextv1alpha1.RuntimeSpec{Image: "runtime"},
						},
						Graders: graders,
					}},
				},
			}
			startedAt := time.Now()
			records := (Runner{
				Client: cluster,
				Logs:   logs,
				Options: RunnerOptions{
					PollInterval: time.Millisecond,
					InvocationID: name,
					Judge:        judge,
				},
			}).RunSuite(context.Background(), suite)
			if elapsed := time.Since(startedAt); elapsed > time.Second {
				t.Fatalf("case timeout did not bound %s stage: %s", name, elapsed)
			}
			if len(records) != 1 || records[0].Pass ||
				!containsCollectionError(records[0].CollectionErrors, context.DeadlineExceeded.Error()) {
				t.Fatalf("%s stage did not fail on case timeout: %#v", name, records)
			}
		})
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

	finished := (Runner{}).finishRecord(
		context.Background(),
		&record,
		item,
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
			requirements := requirementsForGraders([]Grader{grader})
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

type terminalClient struct {
	client.Client
	deletes int
	podGets int
	phase   kontextv1alpha1.AgentRunPhase
	message string
}

func (cluster *terminalClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if err := cluster.Client.Create(ctx, object, options...); err != nil {
		return err
	}
	run, ok := object.(*kontextv1alpha1.AgentRun)
	if !ok {
		return nil
	}
	exitCode := int32(0)
	pod := &corev1.Pod{}
	pod.Namespace = run.Namespace
	pod.Name = "pod-" + run.Name
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "runtime",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: exitCode,
		}},
	}}
	return cluster.Client.Create(ctx, pod)
}

func (cluster *terminalClient) Get(ctx context.Context, key types.NamespacedName, object client.Object, options ...client.GetOption) error {
	if err := cluster.Client.Get(ctx, key, object, options...); err != nil {
		return err
	}
	if run, ok := object.(*kontextv1alpha1.AgentRun); ok {
		phase := cluster.phase
		if phase == "" {
			phase = kontextv1alpha1.AgentRunPhaseSucceeded
		}
		run.Status.Phase = phase
		run.Status.Message = cluster.message
		if run.Status.Message == "" {
			run.Status.Message = "successful status message"
		}
		run.Status.PodName = "pod-" + run.Name
		run.Status.Result = "sensitive model output"
		value := int64(7)
		run.Status.Usage = &kontextv1alpha1.UsageStatus{Tokens: &value}
		run.Status.Output = &kontextv1alpha1.OutputStatus{
			MediaType: "text/plain",
			Value:     runtime.RawExtension{Raw: []byte(`"sensitive model output"`)},
		}
	}
	if pod, ok := object.(*corev1.Pod); ok {
		cluster.podGets++
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "runtime",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0,
				Message:  successfulEnvelopeMessage(),
			}},
		}}
	}
	return nil
}

func (cluster *terminalClient) Delete(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
	cluster.deletes++
	return cluster.Client.Delete(ctx, object, options...)
}

type createFailClient struct {
	client.Client
	deletes int
}

func (cluster *createFailClient) Create(context.Context, client.Object, ...client.CreateOption) error {
	return apierrors.NewForbidden(
		schema.GroupResource{Group: "kontext.dev", Resource: "agentruns"},
		"case",
		errors.New("forbidden"),
	)
}

func (cluster *createFailClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	cluster.deletes++
	return nil
}

type staticLogs struct {
	data      []byte
	err       error
	truncated bool
	calls     int
}

func (logs *staticLogs) Fetch(context.Context, string, string, string) (LogCollection, error) {
	logs.calls++
	return LogCollection{
		Data:      append([]byte(nil), logs.data...),
		Truncated: logs.truncated,
	}, logs.err
}

type orderingJudge struct {
	t      *testing.T
	called bool
}

func (judge *orderingJudge) Evaluate(_ context.Context, observation JudgeObservation) (JudgeResult, error) {
	judge.called = true
	if len(observation.Grades) == 0 {
		judge.t.Fatal("judge ran before deterministic graders")
	}
	return JudgeResult{Configured: true, Pass: true, Score: 1, Rationale: "ok"}, nil
}

func successfulLogs(t *testing.T) []byte {
	t.Helper()
	var logs bytes.Buffer
	logs.WriteString(`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"lifecycle","data":{"phase":"started"}}` + "\n")
	return logs.Bytes()
}

func successfulEnvelopeMessage() string {
	output, _ := json.Marshal("private output")
	extension, _ := json.Marshal("secret-extension")
	turns := int32(2)
	tools := int32(1)
	envelope := resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeSucceeded,
		Output: &resultv1alpha1.Output{
			MediaType: "text/plain",
			Value:     output,
		},
		Execution: &resultv1alpha1.Execution{
			Provider:  "private-provider",
			Model:     "model",
			RequestID: "request-secret",
			Turns:     &turns,
			ToolCalls: &tools,
		},
		Artifacts: []resultv1alpha1.Artifact{{
			Name: "artifact-secret", URI: "memory://artifact",
		}},
		Extensions: map[string]json.RawMessage{
			"example.com/private": extension,
		},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func int32Pointer(value int32) *int32 {
	return &value
}

type cleanupContextClient struct {
	client.Client
	deletes          int
	deleteContextErr error
	hadDeadline      bool
}

func (cluster *cleanupContextClient) Delete(
	ctx context.Context,
	object client.Object,
	options ...client.DeleteOption,
) error {
	cluster.deletes++
	cluster.deleteContextErr = ctx.Err()
	_, cluster.hadDeadline = ctx.Deadline()
	return cluster.Client.Delete(ctx, object, options...)
}

type sequencedGetClient struct {
	client.Client
	agentErrors []error
	podErrors   []error
	agentGets   int
	podGets     int
}

func (cluster *sequencedGetClient) Get(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
	options ...client.GetOption,
) error {
	switch object.(type) {
	case *kontextv1alpha1.AgentRun:
		cluster.agentGets++
		if len(cluster.agentErrors) > 0 {
			err := cluster.agentErrors[0]
			cluster.agentErrors = cluster.agentErrors[1:]
			return err
		}
	case *corev1.Pod:
		cluster.podGets++
		if len(cluster.podErrors) > 0 {
			err := cluster.podErrors[0]
			cluster.podErrors = cluster.podErrors[1:]
			return err
		}
	}
	return cluster.Client.Get(ctx, key, object, options...)
}

func newFakeEvalClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

type blockingCreateClient struct {
	client.Client
}

func (cluster *blockingCreateClient) Create(
	ctx context.Context,
	_ client.Object,
	_ ...client.CreateOption,
) error {
	<-ctx.Done()
	return ctx.Err()
}

type blockingPodGetClient struct {
	client.Client
}

func (cluster *blockingPodGetClient) Get(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
	options ...client.GetOption,
) error {
	if _, ok := object.(*corev1.Pod); ok {
		<-ctx.Done()
		return ctx.Err()
	}
	return cluster.Client.Get(ctx, key, object, options...)
}

type blockingLogs struct{}

func (blockingLogs) Fetch(
	ctx context.Context,
	_, _, _ string,
) (LogCollection, error) {
	<-ctx.Done()
	return LogCollection{}, ctx.Err()
}

type blockingJudge struct{}

func (blockingJudge) Evaluate(
	ctx context.Context,
	_ JudgeObservation,
) (JudgeResult, error) {
	<-ctx.Done()
	return JudgeResult{}, ctx.Err()
}

type ambiguousCreateClient struct {
	client.Client
	unowned            bool
	probeNotFoundCount int
	deletes            int
}

func (cluster *ambiguousCreateClient) Create(
	ctx context.Context,
	object client.Object,
	_ ...client.CreateOption,
) error {
	run := object.(*kontextv1alpha1.AgentRun).DeepCopy()
	if cluster.unowned {
		run.Labels = map[string]string{"owner": "user"}
	}
	if err := cluster.Client.Create(ctx, run); err != nil {
		return err
	}
	return apierrors.NewTimeoutError("create response lost", 0)
}

func (cluster *ambiguousCreateClient) Get(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
	options ...client.GetOption,
) error {
	if _, ok := object.(*kontextv1alpha1.AgentRun); ok && cluster.probeNotFoundCount > 0 {
		cluster.probeNotFoundCount--
		return apierrors.NewNotFound(
			schema.GroupResource{Group: "kontext.dev", Resource: "agentruns"},
			key.Name,
		)
	}
	return cluster.Client.Get(ctx, key, object, options...)
}

func (cluster *ambiguousCreateClient) Delete(
	ctx context.Context,
	object client.Object,
	options ...client.DeleteOption,
) error {
	cluster.deletes++
	return cluster.Client.Delete(ctx, object, options...)
}
