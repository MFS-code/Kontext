package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/engine"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/events"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

func TestRunnerCompletesFakeConversation(t *testing.T) {
	emitter := &recordingEmitter{}
	runner := engine.Runner{Emitter: emitter}
	result := runner.Run(context.Background(), baseConfig())

	if result.ExitCode != 0 || result.Envelope.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("unexpected result %#v", result)
	}
	if result.Envelope.Execution == nil ||
		result.Envelope.Execution.Model != "vendor/model@2026:beta" {
		t.Fatalf("model identifier changed: %#v", result.Envelope.Execution)
	}
	if got := resultv1alpha1.ProjectLegacyResult(result.Envelope.Output); got !=
		"Fake provider completed goal: explain the contract" {
		t.Fatalf("unexpected output %q", got)
	}
	if result.Envelope.Usage == nil || result.Envelope.Usage.TotalTokens == nil {
		t.Fatalf("expected normalized usage: %#v", result.Envelope.Usage)
	}
	if !emitter.has(events.TypeLifecycle) ||
		!emitter.has(events.TypeUsage) ||
		!emitter.has(events.TypeOutput) {
		t.Fatalf("missing execution events %#v", emitter.types)
	}
}

func TestRunnerNormalizesProviderFailures(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
		code     string
	}{
		{name: "failure", scenario: provider.FakeScenarioFailure, code: "fake_provider_failure"},
		{name: "malformed", scenario: provider.FakeScenarioMalformed, code: "invalid_provider_response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeConfig := baseConfig()
			runtimeConfig.FakeScenario = test.scenario
			result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
				context.Background(),
				runtimeConfig,
			)
			if result.ExitCode == 0 || result.Envelope.Outcome != resultv1alpha1.OutcomeFailed {
				t.Fatalf("expected failed execution, got %#v", result)
			}
			if result.Envelope.Error == nil || result.Envelope.Error.Code != test.code {
				t.Fatalf("unexpected error %#v", result.Envelope.Error)
			}
		})
	}
}

func TestRunnerDoesNotTreatIncompleteStopReasonsAsSuccess(t *testing.T) {
	tests := []struct {
		reason runtimeapi.StopReason
		code   string
	}{
		{reason: runtimeapi.StopReasonMaxTokens, code: "max_tokens_reached"},
		{reason: runtimeapi.StopReasonContentFilter, code: "content_filtered"},
		{reason: runtimeapi.StopReasonRefusal, code: "provider_refusal"},
		{
			reason: runtimeapi.StopReasonModelContextWindowExceeded,
			code:   "context_window_exceeded",
		},
		{reason: runtimeapi.StopReasonToolUse, code: "invalid_provider_response"},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			response := finalResponse("partial")
			response.StopReason = test.reason
			if test.reason == runtimeapi.StopReasonContentFilter ||
				test.reason == runtimeapi.StopReasonRefusal ||
				test.reason == runtimeapi.StopReasonToolUse {
				response.Message.Content = nil
			}
			selectedProvider := &scriptedProvider{
				responses: []runtimeapi.CompletionResponse{response},
			}
			result := runnerWithTools(
				selectedProvider,
				lookupExecutor(runtimeapi.ToolResult{}),
			).Run(context.Background(), baseConfig())
			if result.Envelope.Error == nil ||
				result.Envelope.Error.Code != test.code {
				t.Fatalf("unexpected result %#v", result.Envelope.Error)
			}
		})
	}
}

func TestRunnerRejectsUnsupportedProvider(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.Provider = "unknown"
	result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
		context.Background(),
		runtimeConfig,
	)
	if result.Envelope.Error == nil || result.Envelope.Error.Code != "unsupported_provider" {
		t.Fatalf("unexpected error %#v", result.Envelope.Error)
	}
}

func TestRunnerDistinguishesInvalidProviderConfiguration(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.FakeScenario = "unknown"
	result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
		context.Background(),
		runtimeConfig,
	)
	if result.Envelope.Error == nil ||
		result.Envelope.Error.Code != "invalid_provider_configuration" {
		t.Fatalf("unexpected error %#v", result.Envelope.Error)
	}
}

func TestRunnerReportsMissingProviderCredentials(t *testing.T) {
	for _, providerName := range []string{"anthropic", "openai", "openai-compatible"} {
		t.Run(providerName, func(t *testing.T) {
			runtimeConfig := baseConfig()
			runtimeConfig.Provider = providerName
			result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
				context.Background(),
				runtimeConfig,
			)
			if result.Envelope.Error == nil ||
				result.Envelope.Error.Code != "missing_provider_credentials" {
				t.Fatalf("unexpected error %#v", result.Envelope.Error)
			}
		})
	}
}

func TestRunnerExecutesToolCallsAndReturnsResultsToProvider(t *testing.T) {
	emitter := &recordingEmitter{}
	selectedProvider := &scriptedProvider{
		responses: []runtimeapi.CompletionResponse{
			{
				Message: runtimeapi.Message{
					Role: runtimeapi.RoleAssistant,
					Content: []runtimeapi.ContentBlock{
						{
							Type: runtimeapi.ContentTypeToolCall,
							ToolCall: &runtimeapi.ToolCall{
								ID:        "call-1",
								Name:      "lookup",
								Arguments: json.RawMessage(`{"query":"status"}`),
							},
						},
					},
				},
				StopReason: runtimeapi.StopReasonToolUse,
				RequestID:  "request-1",
			},
			{
				Message: runtimeapi.Message{
					Role: runtimeapi.RoleAssistant,
					Content: []runtimeapi.ContentBlock{
						{Type: runtimeapi.ContentTypeText, Text: "tool completed"},
					},
				},
				StopReason: runtimeapi.StopReasonEndTurn,
				RequestID:  "request-2",
			},
		},
	}
	executor := &staticToolExecutor{
		definitions: []runtimeapi.ToolDefinition{
			{
				Name:        "lookup",
				Description: "Look up a status.",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		result: runtimeapi.ToolResult{Content: `{"status":"ok"}`},
	}
	runner := engine.Runner{
		Emitter: emitter,
		Resolve: func(config.Config) (provider.Provider, error) {
			return selectedProvider, nil
		},
		ResolveTools: func(config.Config) (engine.ToolExecutor, error) {
			return executor, nil
		},
	}
	result := runner.Run(context.Background(), baseConfig())
	if result.ExitCode != 0 {
		t.Fatalf("unexpected failure %#v", result.Envelope.Error)
	}
	if result.Envelope.Execution == nil ||
		result.Envelope.Execution.ToolCalls == nil ||
		*result.Envelope.Execution.ToolCalls != 1 {
		t.Fatalf("unexpected execution metadata %#v", result.Envelope.Execution)
	}
	if !emitter.has(events.TypeTool) {
		t.Fatalf("expected tool event, got %#v", emitter.types)
	}
	if result.Envelope.Execution.Turns == nil ||
		*result.Envelope.Execution.Turns != 2 {
		t.Fatalf("unexpected turn count %#v", result.Envelope.Execution)
	}
	if len(selectedProvider.requests) != 2 {
		t.Fatalf("expected two provider requests, got %d", len(selectedProvider.requests))
	}
	results := runtimeapi.MessageToolResults(
		selectedProvider.requests[1].Messages[len(selectedProvider.requests[1].Messages)-1],
	)
	if len(results) != 1 || results[0].Content != `{"status":"ok"}` {
		t.Fatalf("tool result was not returned to provider: %#v", results)
	}
}

func TestRunnerClosesToolsWithFreshContextAndReportsCleanupFailure(t *testing.T) {
	executor := &closingToolExecutor{
		staticToolExecutor: *lookupExecutor(runtimeapi.ToolResult{Content: "ok"}),
		close: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Fatalf("cleanup received an already-cancelled context: %v", ctx.Err())
			}
			return errors.New("close failed")
		},
	}
	selectedProvider := &scriptedProvider{responses: []runtimeapi.CompletionResponse{
		toolCallResponse("call-1"),
		finalResponse("done"),
	}}
	emitter := &recordingEmitter{}
	runner := runnerWithTools(selectedProvider, executor)
	runner.Emitter = emitter
	result := runner.Run(context.Background(), baseConfig())
	if result.ExitCode == 0 || result.Envelope.Error == nil ||
		result.Envelope.Error.Code != "tool_cleanup_failed" {
		t.Fatalf("cleanup failure did not fail successful run: %#v", result)
	}
	if executor.closeCalls != 1 {
		t.Fatalf("expected one cleanup call, got %d", executor.closeCalls)
	}
	if result.Envelope.Output == nil || string(result.Envelope.Output.Value) != `"done"` {
		t.Fatalf("cleanup failure discarded completed output: %#v", result.Envelope.Output)
	}
	if !emitter.has(events.TypeOutput) {
		t.Fatalf("completed output was not emitted after failed cleanup: %#v", emitter.types)
	}
	if !emitter.has(events.TypeError) {
		t.Fatalf("cleanup failure was not observable: %#v", emitter.types)
	}
}

func TestRunnerCleanupFailureDoesNotReplaceExistingFailure(t *testing.T) {
	executor := &closingToolExecutor{
		staticToolExecutor: *lookupExecutor(runtimeapi.ToolResult{}),
		close: func(context.Context) error {
			return errors.New("close failed")
		},
	}
	selectedProvider := &scriptedProvider{responses: []runtimeapi.CompletionResponse{
		{
			Message: runtimeapi.Message{
				Role:    runtimeapi.RoleAssistant,
				Content: []runtimeapi.ContentBlock{{Type: runtimeapi.ContentTypeText, Text: "partial"}},
			},
			StopReason: runtimeapi.StopReasonMaxTokens,
		},
	}}
	result := runnerWithTools(selectedProvider, executor).Run(context.Background(), baseConfig())
	if result.Envelope.Error == nil || result.Envelope.Error.Code != "max_tokens_reached" {
		t.Fatalf("cleanup replaced existing failure: %#v", result.Envelope.Error)
	}
	if executor.closeCalls != 1 {
		t.Fatalf("expected one cleanup call, got %d", executor.closeCalls)
	}
}

func TestRunnerRequiresOptInForToolOutputEvents(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			emitter := &dataRecordingEmitter{}
			selectedProvider := &scriptedProvider{
				responses: []runtimeapi.CompletionResponse{
					toolCallResponse("call-1"),
					finalResponse("done"),
				},
			}
			executor := lookupExecutor(runtimeapi.ToolResult{Content: "sensitive output"})
			runner := runnerWithTools(selectedProvider, executor)
			runner.Emitter = emitter
			runtimeConfig := baseConfig()
			runtimeConfig.EmitToolOutput = enabled
			result := runner.Run(context.Background(), runtimeConfig)
			if result.ExitCode != 0 {
				t.Fatalf("unexpected failure %#v", result.Envelope.Error)
			}
			toolEvent := emitter.first(events.TypeTool)
			_, hasOutput := toolEvent["output"]
			if hasOutput != enabled {
				t.Fatalf("output presence=%t, want %t; event=%#v", hasOutput, enabled, toolEvent)
			}
		})
	}
}

func TestRunnerEnforcesTurnAndToolCallLimits(t *testing.T) {
	t.Run("turn limit", func(t *testing.T) {
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{
				toolCallResponse("call-1"),
			},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{Content: "ok"})
		runtimeConfig := baseConfig()
		maxTurns := int64(1)
		runtimeConfig.MaxTurns = &maxTurns
		result := runnerWithTools(selectedProvider, executor).Run(
			context.Background(),
			runtimeConfig,
		)
		if result.Envelope.Error == nil ||
			result.Envelope.Error.Code != "turn_limit_exceeded" {
			t.Fatalf("unexpected result %#v", result.Envelope.Error)
		}
		if len(executor.calls) != 0 {
			t.Fatalf("tool ran without a remaining provider turn")
		}
	})

	t.Run("tool call limit", func(t *testing.T) {
		response := toolCallResponse("call-1")
		response.Message.Content = append(
			response.Message.Content,
			runtimeapi.ContentBlock{
				Type: runtimeapi.ContentTypeToolCall,
				ToolCall: &runtimeapi.ToolCall{
					ID:        "call-2",
					Name:      "lookup",
					Arguments: json.RawMessage(`{}`),
				},
			},
		)
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{response},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{Content: "ok"})
		runtimeConfig := baseConfig()
		maxToolCalls := int64(1)
		runtimeConfig.MaxToolCalls = &maxToolCalls
		result := runnerWithTools(selectedProvider, executor).Run(
			context.Background(),
			runtimeConfig,
		)
		if result.Envelope.Error == nil ||
			result.Envelope.Error.Code != "tool_call_limit_exceeded" {
			t.Fatalf("unexpected result %#v", result.Envelope.Error)
		}
		if len(executor.calls) != 0 {
			t.Fatalf("oversized batch executed %d tools", len(executor.calls))
		}
	})

	t.Run("tool call batch exactly fits remaining limit", func(t *testing.T) {
		response := toolCallResponse("call-1")
		response.Message.Content = append(
			response.Message.Content,
			runtimeapi.ContentBlock{
				Type: runtimeapi.ContentTypeToolCall,
				ToolCall: &runtimeapi.ToolCall{
					ID:        "call-2",
					Name:      "lookup",
					Arguments: json.RawMessage(`{}`),
				},
			},
		)
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{
				response,
				finalResponse("done"),
			},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{Content: "ok"})
		runtimeConfig := baseConfig()
		maxToolCalls := int64(2)
		runtimeConfig.MaxToolCalls = &maxToolCalls
		result := runnerWithTools(selectedProvider, executor).Run(
			context.Background(),
			runtimeConfig,
		)
		if result.ExitCode != 0 || len(executor.calls) != 2 {
			t.Fatalf("exact-fit batch failed: result=%#v calls=%d", result, len(executor.calls))
		}
	})

	t.Run("tool call batch exceeds remaining limit", func(t *testing.T) {
		secondBatch := toolCallResponse("call-2")
		secondBatch.Message.Content = append(
			secondBatch.Message.Content,
			runtimeapi.ContentBlock{
				Type: runtimeapi.ContentTypeToolCall,
				ToolCall: &runtimeapi.ToolCall{
					ID:        "call-3",
					Name:      "lookup",
					Arguments: json.RawMessage(`{}`),
				},
			},
		)
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{
				toolCallResponse("call-1"),
				secondBatch,
			},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{Content: "ok"})
		runtimeConfig := baseConfig()
		maxToolCalls := int64(2)
		runtimeConfig.MaxToolCalls = &maxToolCalls
		result := runnerWithTools(selectedProvider, executor).Run(
			context.Background(),
			runtimeConfig,
		)
		if result.Envelope.Error == nil ||
			result.Envelope.Error.Code != "tool_call_limit_exceeded" {
			t.Fatalf("unexpected result %#v", result.Envelope.Error)
		}
		if len(executor.calls) != 1 {
			t.Fatalf("oversized second batch changed call count to %d", len(executor.calls))
		}
	})
}

func TestRunnerBoundsToolOutputAndReturnsToolErrors(t *testing.T) {
	t.Run("per-result and total output limits", func(t *testing.T) {
		response := toolCallResponse("call-1")
		response.Message.Content = append(
			response.Message.Content,
			runtimeapi.ContentBlock{
				Type: runtimeapi.ContentTypeToolCall,
				ToolCall: &runtimeapi.ToolCall{
					ID:        "call-2",
					Name:      "lookup",
					Arguments: json.RawMessage(`{}`),
				},
			},
		)
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{
				response,
				finalResponse("done"),
			},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{Content: "abcdefgh"})
		runtimeConfig := baseConfig()
		perResult := int64(4)
		totalOutput := int64(6)
		runtimeConfig.MaxToolResultBytes = &perResult
		runtimeConfig.MaxTotalToolOutputBytes = &totalOutput
		result := runnerWithTools(selectedProvider, executor).Run(
			context.Background(),
			runtimeConfig,
		)
		if result.ExitCode != 0 {
			t.Fatalf("unexpected failure %#v", result.Envelope.Error)
		}
		results := runtimeapi.MessageToolResults(
			selectedProvider.requests[1].Messages[len(selectedProvider.requests[1].Messages)-1],
		)
		if len(results) != 2 ||
			results[0].Content != "abcd" ||
			results[1].Content != "ab" ||
			!results[0].Truncated ||
			!results[1].Truncated {
			t.Fatalf("unexpected bounded results %#v", results)
		}
	})

	t.Run("tiny result limit keeps structured content valid", func(t *testing.T) {
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{
				toolCallResponse("call-1"),
				finalResponse("done"),
			},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{
			Content: `{"status":"ok"}`,
		})
		runtimeConfig := baseConfig()
		perResult := int64(1)
		runtimeConfig.MaxToolResultBytes = &perResult
		result := runnerWithTools(selectedProvider, executor).Run(
			context.Background(),
			runtimeConfig,
		)
		if result.ExitCode != 0 {
			t.Fatalf("unexpected failure %#v", result.Envelope.Error)
		}
		results := runtimeapi.MessageToolResults(
			selectedProvider.requests[1].Messages[len(selectedProvider.requests[1].Messages)-1],
		)
		if len(results) != 1 ||
			results[0].Content != "0" ||
			!json.Valid([]byte(results[0].Content)) ||
			!results[0].Truncated {
			t.Fatalf("unexpected tiny structured result %#v", results)
		}
	})

	t.Run("structured shell result remains valid near boundary", func(t *testing.T) {
		shellResult := `{"exitCode":0,"stdout":"123456","stderr":""}`
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{
				toolCallResponse("call-1"),
				finalResponse("done"),
			},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{Content: shellResult})
		runtimeConfig := baseConfig()
		perResult := int64(len(shellResult) - 1)
		runtimeConfig.MaxToolResultBytes = &perResult
		result := runnerWithTools(selectedProvider, executor).Run(
			context.Background(),
			runtimeConfig,
		)
		if result.ExitCode != 0 {
			t.Fatalf("unexpected failure %#v", result.Envelope.Error)
		}
		results := runtimeapi.MessageToolResults(
			selectedProvider.requests[1].Messages[len(selectedProvider.requests[1].Messages)-1],
		)
		if len(results) != 1 ||
			!json.Valid([]byte(results[0].Content)) ||
			int64(len(results[0].Content)) > perResult ||
			!results[0].Truncated {
			t.Fatalf("unexpected structured result %#v", results)
		}
		var bounded map[string]string
		if err := json.Unmarshal([]byte(results[0].Content), &bounded); err != nil ||
			bounded["partial"] == "" {
			t.Fatalf("structured prefix was not preserved: content=%q err=%v", results[0].Content, err)
		}
	})

	t.Run("tool error returned to provider", func(t *testing.T) {
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{
				toolCallResponse("call-error"),
				finalResponse("recovered"),
			},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{
			Content:   "lookup failed",
			IsError:   true,
			ErrorCode: "lookup_failed",
		})
		result := runnerWithTools(selectedProvider, executor).Run(
			context.Background(),
			baseConfig(),
		)
		if result.ExitCode != 0 {
			t.Fatalf("unexpected failure %#v", result.Envelope.Error)
		}
		results := runtimeapi.MessageToolResults(
			selectedProvider.requests[1].Messages[len(selectedProvider.requests[1].Messages)-1],
		)
		if len(results) != 1 ||
			!results[0].IsError ||
			results[0].ErrorCode != "lookup_failed" {
			t.Fatalf("tool error was not returned: %#v", results)
		}
	})
}

func TestRunnerSaturatesCumulativeUsageWithoutOverflow(t *testing.T) {
	first := toolCallResponse("call-1")
	nearMaximum := int64(math.MaxInt64 - 1)
	first.Usage.TotalTokens = &nearMaximum
	second := finalResponse("done")
	ten := int64(10)
	second.Usage.TotalTokens = &ten
	selectedProvider := &scriptedProvider{
		responses: []runtimeapi.CompletionResponse{first, second},
	}
	result := runnerWithTools(
		selectedProvider,
		lookupExecutor(runtimeapi.ToolResult{Content: "ok"}),
	).Run(context.Background(), baseConfig())
	if result.ExitCode != 0 {
		t.Fatalf("unexpected failure %#v", result.Envelope.Error)
	}
	if result.Envelope.Usage == nil ||
		result.Envelope.Usage.TotalTokens == nil ||
		*result.Envelope.Usage.TotalTokens != math.MaxInt64 {
		t.Fatalf("usage did not saturate safely: %#v", result.Envelope.Usage)
	}
}

func TestRunnerAggregatesReasoningUsageAcrossTurns(t *testing.T) {
	first := toolCallResponse("call-1")
	zero := int64(0)
	firstOutput := int64(5)
	first.Usage.OutputTokens = &firstOutput
	first.Usage.ReasoningTokens = &zero

	second := finalResponse("done")
	secondOutput := int64(10)
	secondReasoning := int64(7)
	second.Usage.OutputTokens = &secondOutput
	second.Usage.ReasoningTokens = &secondReasoning

	selectedProvider := &scriptedProvider{
		responses: []runtimeapi.CompletionResponse{first, second},
	}
	result := runnerWithTools(
		selectedProvider,
		lookupExecutor(runtimeapi.ToolResult{Content: "ok"}),
	).Run(context.Background(), baseConfig())
	if result.ExitCode != 0 {
		t.Fatalf("unexpected failure %#v", result.Envelope.Error)
	}
	if result.Envelope.Usage == nil ||
		result.Envelope.Usage.OutputTokens == nil ||
		*result.Envelope.Usage.OutputTokens != 15 ||
		result.Envelope.Usage.ReasoningTokens == nil ||
		*result.Envelope.Usage.ReasoningTokens != 7 {
		t.Fatalf("reasoning usage did not aggregate: %#v", result.Envelope.Usage)
	}
}

func TestRunnerHasNoImplicitWallclockDeadline(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.FakeScenario = provider.FakeScenarioDelay
	runtimeConfig.FakeDelay = 30 * time.Millisecond
	runtimeConfig.WallclockBudget = nil

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(ctx, runtimeConfig)
	if result.ExitCode != 0 {
		t.Fatalf("delay should succeed without configured wallclock: %#v", result.Envelope.Error)
	}
}

func TestRunnerLeavesWallclockAuthorityWithController(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.FakeScenario = provider.FakeScenarioDelay
	runtimeConfig.FakeDelay = 30 * time.Millisecond
	wallclock := 10 * time.Millisecond
	runtimeConfig.WallclockBudget = &wallclock

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(ctx, runtimeConfig)
	if result.ExitCode != 0 {
		t.Fatalf("runtime must not race controller wallclock enforcement: %#v", result.Envelope.Error)
	}
}

func TestRunnerHandlesParentDeadlineAndCancellation(t *testing.T) {
	t.Run("parent deadline", func(t *testing.T) {
		runtimeConfig := baseConfig()
		runtimeConfig.FakeScenario = provider.FakeScenarioDelay
		runtimeConfig.FakeDelay = time.Second
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
			ctx,
			runtimeConfig,
		)
		if result.Envelope.Error == nil || result.Envelope.Error.Code != "deadline_exceeded" {
			t.Fatalf("unexpected deadline result %#v", result.Envelope.Error)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		runtimeConfig := baseConfig()
		runtimeConfig.FakeScenario = provider.FakeScenarioDelay
		runtimeConfig.FakeDelay = time.Second
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(ctx, runtimeConfig)
		if result.Envelope.Error == nil || result.Envelope.Error.Code != "cancelled" {
			t.Fatalf("unexpected cancellation result %#v", result.Envelope.Error)
		}
	})
}

func baseConfig() config.Config {
	return config.Config{
		RunName:      "run-1",
		AgentName:    "agent-1",
		Goal:         "explain the contract",
		Provider:     "fake",
		Model:        "vendor/model@2026:beta",
		FakeScenario: provider.FakeScenarioSuccess,
	}
}

func runnerWithTools(
	selectedProvider provider.Provider,
	executor engine.ToolExecutor,
) engine.Runner {
	return engine.Runner{
		Emitter: &recordingEmitter{},
		Resolve: func(config.Config) (provider.Provider, error) {
			return selectedProvider, nil
		},
		ResolveTools: func(config.Config) (engine.ToolExecutor, error) {
			return executor, nil
		},
	}
}

func lookupExecutor(result runtimeapi.ToolResult) *staticToolExecutor {
	return &staticToolExecutor{
		definitions: []runtimeapi.ToolDefinition{
			{
				Name:        "lookup",
				Description: "Look up a status.",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		result: result,
	}
}

func toolCallResponse(callID string) runtimeapi.CompletionResponse {
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{
					Type: runtimeapi.ContentTypeToolCall,
					ToolCall: &runtimeapi.ToolCall{
						ID:        callID,
						Name:      "lookup",
						Arguments: json.RawMessage(`{}`),
					},
				},
			},
		},
		StopReason: runtimeapi.StopReasonToolUse,
		RequestID:  "tool-request",
	}
}

func finalResponse(text string) runtimeapi.CompletionResponse {
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{Type: runtimeapi.ContentTypeText, Text: text},
			},
		},
		StopReason: runtimeapi.StopReasonEndTurn,
		RequestID:  "final-request",
	}
}

type recordingEmitter struct {
	types []events.Type
}

type dataRecordingEmitter struct {
	events []recordedEvent
}

type recordedEvent struct {
	eventType events.Type
	data      any
}

type scriptedProvider struct {
	responses []runtimeapi.CompletionResponse
	requests  []runtimeapi.CompletionRequest
}

func (provider *scriptedProvider) Name() string {
	return "scripted"
}

func (provider *scriptedProvider) Complete(
	_ context.Context,
	request runtimeapi.CompletionRequest,
) (runtimeapi.CompletionResponse, error) {
	index := len(provider.requests)
	if index >= len(provider.responses) {
		panic("unexpected provider completion")
	}
	provider.requests = append(provider.requests, request)
	return provider.responses[index], nil
}

type staticToolExecutor struct {
	definitions []runtimeapi.ToolDefinition
	result      runtimeapi.ToolResult
	err         error
	calls       []runtimeapi.ToolCall
}

type closingToolExecutor struct {
	staticToolExecutor
	close      func(context.Context) error
	closeCalls int
}

func (executor *closingToolExecutor) Close(ctx context.Context) error {
	executor.closeCalls++
	return executor.close(ctx)
}

func (executor *staticToolExecutor) Definitions() []runtimeapi.ToolDefinition {
	return runtimeapi.CloneToolDefinitions(executor.definitions)
}

func (executor *staticToolExecutor) Execute(
	_ context.Context,
	call runtimeapi.ToolCall,
) (runtimeapi.ToolResult, error) {
	executor.calls = append(executor.calls, call)
	result := executor.result
	result.CallID = call.ID
	result.Name = call.Name
	return result, executor.err
}

func (emitter *recordingEmitter) Emit(eventType events.Type, _ any) {
	emitter.types = append(emitter.types, eventType)
}

func (emitter *dataRecordingEmitter) Emit(eventType events.Type, data any) {
	emitter.events = append(emitter.events, recordedEvent{
		eventType: eventType,
		data:      data,
	})
}

func (emitter *dataRecordingEmitter) first(eventType events.Type) map[string]any {
	for _, event := range emitter.events {
		if event.eventType == eventType {
			data, _ := event.data.(map[string]any)
			return data
		}
	}
	return nil
}

func (emitter *recordingEmitter) has(eventType events.Type) bool {
	for _, candidate := range emitter.types {
		if candidate == eventType {
			return true
		}
	}
	return false
}
