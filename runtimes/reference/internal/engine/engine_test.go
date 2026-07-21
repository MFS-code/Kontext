package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/engine"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/events"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

func TestRunnerCompletesFakeConversation(t *testing.T) {
	emitter := &recordingEmitter{}
	runner := runnerWithoutTools(emitter)
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

func TestRunnerRequiresContextAwareToolResolver(t *testing.T) {
	emitter := &recordingEmitter{}
	result := (engine.Runner{Emitter: emitter}).Run(context.Background(), baseConfig())

	if result.ExitCode == 0 || result.Envelope.Error == nil {
		t.Fatalf("expected resolver configuration failure, got %#v", result)
	}
	if result.Envelope.Error.Code != "invalid_tool_configuration" ||
		result.Envelope.Error.Message != "tool resolver is required" {
		t.Fatalf("unexpected resolver failure %#v", result.Envelope.Error)
	}
	if got := emitter.count(events.TypeError); got != 1 {
		t.Fatalf("expected one terminal error event, got %d", got)
	}
}

func TestRunnerClosesEveryResolvedExecutorExactlyOnce(t *testing.T) {
	tests := []struct {
		name           string
		returnExecutor bool
		resolverError  error
		wantExitCode   int
		wantCloseCalls int
		wantErrorCode  string
	}{
		{
			name:           "executor and error",
			returnExecutor: true,
			resolverError:  errors.New("resolve failed"),
			wantExitCode:   1,
			wantCloseCalls: 1,
			wantErrorCode:  "invalid_tool_configuration",
		},
		{
			name: "nil and coded error",
			resolverError: &runtimeapi.CodedError{
				Code:    "unknown_tool",
				Message: "resolve failed",
			},
			wantExitCode:   1,
			wantCloseCalls: 0,
			wantErrorCode:  "unknown_tool",
		},
		{
			name:           "successful executor",
			returnExecutor: true,
			wantExitCode:   0,
			wantCloseCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &closingToolExecutor{
				close: func(context.Context) error { return nil },
			}
			runner := engine.Runner{
				Emitter: &recordingEmitter{},
				ResolveToolsContext: func(
					context.Context,
					config.Config,
				) (engine.ToolExecutor, error) {
					if !test.returnExecutor {
						return nil, test.resolverError
					}
					return executor, test.resolverError
				},
			}

			result := runner.Run(context.Background(), baseConfig())
			if result.ExitCode != test.wantExitCode {
				t.Fatalf("exit code=%d, want %d; result=%#v", result.ExitCode, test.wantExitCode, result)
			}
			if executor.closeCalls != test.wantCloseCalls {
				t.Fatalf("close calls=%d, want %d", executor.closeCalls, test.wantCloseCalls)
			}
			if test.resolverError != nil &&
				(result.Envelope.Error == nil ||
					result.Envelope.Error.Code != test.wantErrorCode ||
					result.Envelope.Error.Message != test.resolverError.Error()) {
				t.Fatalf("resolver error was not preserved: %#v", result.Envelope.Error)
			}
		})
	}
}

func TestRunnerReportsCleanupAfterResolverFailure(t *testing.T) {
	emitter := &dataRecordingEmitter{}
	executor := &closingToolExecutor{
		close: func(context.Context) error {
			return errors.New("close failed")
		},
	}
	runner := engine.Runner{
		Emitter: emitter,
		ResolveToolsContext: func(
			context.Context,
			config.Config,
		) (engine.ToolExecutor, error) {
			return executor, errors.New("resolve failed")
		},
	}

	result := runner.Run(context.Background(), baseConfig())
	if result.Envelope.Error == nil ||
		result.Envelope.Error.Code != "invalid_tool_configuration" ||
		result.Envelope.Error.Message != "resolve failed" {
		t.Fatalf("cleanup replaced resolver failure: %#v", result.Envelope.Error)
	}
	if executor.closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1", executor.closeCalls)
	}

	var errorCodes []string
	for _, event := range emitter.events {
		switch event.eventType {
		case events.TypeError:
			data, ok := event.data.(map[string]any)
			if !ok {
				t.Fatalf("unexpected error event data %#v", event.data)
			}
			code, _ := data["code"].(string)
			errorCodes = append(errorCodes, code)
		case events.TypeOutput:
			t.Fatal("resolver failure emitted terminal output")
		}
	}
	if len(errorCodes) != 2 ||
		errorCodes[0] != "invalid_tool_configuration" ||
		errorCodes[1] != "tool_cleanup_failed" {
		t.Fatalf("unexpected error event ordering %#v", errorCodes)
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
			result := runnerWithoutTools(&recordingEmitter{}).Run(
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
	result := runnerWithoutTools(&recordingEmitter{}).Run(
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
	result := runnerWithoutTools(&recordingEmitter{}).Run(
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
			result := runnerWithoutTools(&recordingEmitter{}).Run(
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
		ResolveToolsContext: func(context.Context, config.Config) (engine.ToolExecutor, error) {
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

func TestRunnerClosesToolsBeforeEmittingFinalOutput(t *testing.T) {
	closed := false
	executor := &closingToolExecutor{
		staticToolExecutor: *lookupExecutor(runtimeapi.ToolResult{}),
		close: func(context.Context) error {
			closed = true
			return nil
		},
	}
	runner := runnerWithTools(
		&scriptedProvider{responses: []runtimeapi.CompletionResponse{finalResponse("done")}},
		executor,
	)
	runner.Emitter = emitterFunc(func(eventType events.Type, _ any) {
		if eventType == events.TypeOutput && !closed {
			t.Fatal("final output emitted before tool cleanup")
		}
	})

	result := runner.Run(context.Background(), baseConfig())
	if result.ExitCode != 0 || !closed {
		t.Fatalf("cleanup ordering failed: result=%#v closed=%t", result, closed)
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

func TestRunnerHasNoImplicitWallclockDeadline(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.FakeScenario = provider.FakeScenarioDelay
	runtimeConfig.FakeDelay = 30 * time.Millisecond
	runtimeConfig.WallclockBudget = nil

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := runnerWithoutTools(&recordingEmitter{}).Run(ctx, runtimeConfig)
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
	result := runnerWithoutTools(&recordingEmitter{}).Run(ctx, runtimeConfig)
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

		result := runnerWithoutTools(&recordingEmitter{}).Run(
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

		result := runnerWithoutTools(&recordingEmitter{}).Run(ctx, runtimeConfig)
		if result.Envelope.Error == nil || result.Envelope.Error.Code != "cancelled" {
			t.Fatalf("unexpected cancellation result %#v", result.Envelope.Error)
		}
	})
}
