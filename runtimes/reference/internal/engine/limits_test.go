package engine_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"unicode/utf8"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

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

func TestRunnerPreservesTokenBudgetBoundarySemantics(t *testing.T) {
	const budget = int64(10)

	t.Run("terminal response may equal budget", func(t *testing.T) {
		response := finalResponse("done")
		response.Usage.TotalTokens = int64Pointer(budget)
		result := runnerWithTools(
			&scriptedProvider{responses: []runtimeapi.CompletionResponse{response}},
			lookupExecutor(runtimeapi.ToolResult{}),
		).Run(context.Background(), configWithTokenBudget(budget))
		if result.ExitCode != 0 {
			t.Fatalf("exact-budget terminal response failed: %#v", result.Envelope.Error)
		}
	})

	t.Run("tool follow-up requires remaining budget", func(t *testing.T) {
		response := toolCallResponse("call-1")
		response.Usage.TotalTokens = int64Pointer(budget)
		executor := lookupExecutor(runtimeapi.ToolResult{Content: "ok"})
		result := runnerWithTools(
			&scriptedProvider{responses: []runtimeapi.CompletionResponse{response}},
			executor,
		).Run(context.Background(), configWithTokenBudget(budget))
		if result.Envelope.Error == nil ||
			result.Envelope.Error.Code != "token_limit_exceeded" {
			t.Fatalf("unexpected exact-budget tool result %#v", result.Envelope.Error)
		}
		if len(executor.calls) != 0 {
			t.Fatalf("tool executed without remaining token budget")
		}
	})

	t.Run("response over budget fails", func(t *testing.T) {
		response := finalResponse("done")
		response.Usage.TotalTokens = int64Pointer(budget + 1)
		result := runnerWithTools(
			&scriptedProvider{responses: []runtimeapi.CompletionResponse{response}},
			lookupExecutor(runtimeapi.ToolResult{}),
		).Run(context.Background(), configWithTokenBudget(budget))
		if result.Envelope.Error == nil ||
			result.Envelope.Error.Code != "token_limit_exceeded" {
			t.Fatalf("unexpected over-budget result %#v", result.Envelope.Error)
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
			results[0].Content != "{}" ||
			results[1].Content != "{}" ||
			int64(len(results[0].Content)+len(results[1].Content)) > totalOutput ||
			int64(len(results[0].Content)) > perResult ||
			int64(len(results[1].Content)) > perResult ||
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
			int64(len(results[0].Content)) > perResult ||
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

	t.Run("plain UTF-8 result uses partial envelope", func(t *testing.T) {
		selectedProvider := &scriptedProvider{
			responses: []runtimeapi.CompletionResponse{
				toolCallResponse("call-1"),
				finalResponse("done"),
			},
		}
		executor := lookupExecutor(runtimeapi.ToolResult{Content: "éééééééééé"})
		runtimeConfig := baseConfig()
		perResult := int64(18)
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
			!results[0].Truncated ||
			int64(len(results[0].Content)) > perResult ||
			!utf8.ValidString(results[0].Content) ||
			!json.Valid([]byte(results[0].Content)) {
			t.Fatalf("unexpected UTF-8 result %#v", results)
		}
		var bounded struct {
			Partial string `json:"partial"`
		}
		if err := json.Unmarshal([]byte(results[0].Content), &bounded); err != nil ||
			bounded.Partial == "" ||
			!utf8.ValidString(bounded.Partial) {
			t.Fatalf("unexpected partial envelope content=%q err=%v", results[0].Content, err)
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

	t.Run("cumulative output is checked between calls", func(t *testing.T) {
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
		executor := lookupExecutor(runtimeapi.ToolResult{Content: "ok"})
		runtimeConfig := baseConfig()
		totalOutput := int64(2)
		runtimeConfig.MaxTotalToolOutputBytes = &totalOutput
		result := runnerWithTools(
			&scriptedProvider{responses: []runtimeapi.CompletionResponse{response}},
			executor,
		).Run(context.Background(), runtimeConfig)
		if result.Envelope.Error == nil ||
			result.Envelope.Error.Code != "tool_output_limit_exceeded" {
			t.Fatalf("unexpected cumulative output result %#v", result.Envelope.Error)
		}
		if len(executor.calls) != 1 {
			t.Fatalf("expected one call before output exhaustion, got %d", len(executor.calls))
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
