package eval

import (
	"context"
	"errors"
	"io"
	"net/url"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

func TestRunnerRetriesTransientAgentRunAndPodGets(t *testing.T) {
	terminal := &terminalClient{Client: newFakeEvalClient(t)}
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
	terminal := &terminalClient{Client: newFakeEvalClient(t)}
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

func TestKubernetesErrorClassification(t *testing.T) {
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
			classification := classifyKubernetesError(err)
			if !classification.retryableRead || !classification.ambiguousWrite {
				t.Fatalf("error was not classified transient: %v", err)
			}
		})
	}
	if classification := classifyKubernetesError(apierrors.NewBadRequest("permanent")); classification.retryableRead ||
		classification.ambiguousWrite {
		t.Fatal("permanent error was classified transient")
	}
	if classification := classifyKubernetesError(apierrors.NewUnauthorized("permanent")); classification.retryableRead ||
		classification.ambiguousWrite {
		t.Fatal("unauthorized error was classified transient")
	}
	unknown := classifyKubernetesError(errors.New("write result unknown"))
	if unknown.retryableRead || !unknown.ambiguousWrite {
		t.Fatalf("unknown error classification was unsafe: %#v", unknown)
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
