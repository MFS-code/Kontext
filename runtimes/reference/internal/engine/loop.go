package engine

import (
	"context"
	"fmt"
	"time"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/conversation"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/events"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

type loopState struct {
	conversation         *conversation.State
	usage                runtimeapi.Usage
	consumedTokens       int64
	totalToolOutputBytes int64
	turns                int32
	toolCalls            int32
	lastRequestID        string
	startedAt            time.Time
	now                  func() time.Time
}

func newLoopState(goal string, now func() time.Time) *loopState {
	return &loopState{
		conversation: conversation.New(goal),
		startedAt:    now().UTC(),
		now:          now,
	}
}

func (state *loopState) metadata(
	runtimeConfig config.Config,
	requestID string,
) Metadata {
	return Metadata{
		Provider:    runtimeConfig.Provider,
		Model:       runtimeConfig.Model,
		RequestID:   requestID,
		StartedAt:   state.startedAt,
		CompletedAt: state.now().UTC(),
		Turns:       state.turns,
		ToolCalls:   state.toolCalls,
	}
}

func (state *loopState) failure(
	runner Runner,
	runtimeConfig config.Config,
	code string,
	message string,
	retryable *bool,
	requestID string,
) Result {
	runner.emitError(code, message, retryable)
	result := failed(
		code,
		message,
		retryable,
		state.metadata(runtimeConfig, requestID),
	)
	result.Envelope.Usage = envelopeUsage(state.usage)
	return result
}

func (state *loopState) limitFailure(
	runner Runner,
	runtimeConfig config.Config,
	code string,
	message string,
	requestID string,
) Result {
	return runner.limitFailure(
		code,
		message,
		state.usage,
		state.metadata(runtimeConfig, requestID),
	)
}

type turnOutcomeKind uint8

const (
	turnOutcomePaused turnOutcomeKind = iota
	turnOutcomeFinal
	turnOutcomeToolBatch
	turnOutcomeFailed
)

type turnOutcome struct {
	kind     turnOutcomeKind
	response runtimeapi.CompletionResponse
	calls    []runtimeapi.ToolCall
	result   Result
}

func (runner Runner) providerTurn(
	ctx context.Context,
	runtimeConfig config.Config,
	selectedProvider provider.Provider,
	definitions []runtimeapi.ToolDefinition,
	state *loopState,
) turnOutcome {
	if limitReached(runtimeConfig.MaxTurns, int64(state.turns)) {
		return turnOutcome{
			kind: turnOutcomeFailed,
			result: state.limitFailure(
				runner,
				runtimeConfig,
				"turn_limit_exceeded",
				"maximum provider turns reached before final output",
				state.lastRequestID,
			),
		}
	}

	requestStartedAt := state.now().UTC()
	state.turns++
	response, err := selectedProvider.Complete(ctx, runtimeapi.CompletionRequest{
		Model:     runtimeConfig.Model,
		Messages:  state.conversation.Messages(),
		MaxTokens: remainingTokenBudget(runtimeConfig.TokenBudget, state.consumedTokens),
		Tools:     runtimeapi.CloneToolDefinitions(definitions),
	})
	if err != nil {
		code, message, retryable, requestID := normalizeError(ctx, err)
		return turnOutcome{
			kind: turnOutcomeFailed,
			result: state.failure(
				runner,
				runtimeConfig,
				code,
				message,
				retryable,
				requestID,
			),
		}
	}

	state.lastRequestID = response.RequestID
	if err := runtimeapi.ValidateResponse(response); err != nil {
		message := fmt.Sprintf("provider returned an invalid response: %v", err)
		return turnOutcome{
			kind: turnOutcomeFailed,
			result: state.failure(
				runner,
				runtimeConfig,
				"invalid_provider_response",
				message,
				nil,
				response.RequestID,
			),
		}
	}

	state.usage = addUsage(state.usage, response.Usage)
	state.consumedTokens = saturatingAdd(
		state.consumedTokens,
		measuredTokens(response.Usage),
	)
	runner.emit(events.TypeLifecycle, map[string]any{
		"phase":          "provider_completed",
		"turn":           state.turns,
		"stopReason":     response.StopReason,
		"durationMillis": state.now().UTC().Sub(requestStartedAt).Milliseconds(),
	})
	if usage := envelopeUsage(response.Usage); usage != nil {
		runner.emit(events.TypeUsage, map[string]any{
			"turn":  state.turns,
			"usage": usage,
		})
	}
	if tokenBudgetExceeded(runtimeConfig.TokenBudget, state.consumedTokens) {
		return turnOutcome{
			kind: turnOutcomeFailed,
			result: state.limitFailure(
				runner,
				runtimeConfig,
				"token_limit_exceeded",
				"measured provider usage exceeded the run token budget",
				response.RequestID,
			),
		}
	}

	state.conversation.Append(response.Message)
	calls := runtimeapi.MessageToolCalls(response.Message)
	if len(calls) > 0 {
		if response.StopReason != runtimeapi.StopReasonToolUse {
			message := fmt.Sprintf(
				"provider returned tool calls with stop reason %q",
				response.StopReason,
			)
			return turnOutcome{
				kind: turnOutcomeFailed,
				result: state.failure(
					runner,
					runtimeConfig,
					"invalid_provider_response",
					message,
					nil,
					response.RequestID,
				),
			}
		}
		return turnOutcome{
			kind:     turnOutcomeToolBatch,
			response: response,
			calls:    calls,
		}
	}

	if response.StopReason == runtimeapi.StopReasonPauseTurn {
		return turnOutcome{kind: turnOutcomePaused}
	}
	if response.StopReason != runtimeapi.StopReasonEndTurn &&
		response.StopReason != runtimeapi.StopReasonStopSequence {
		code, message := terminalStopFailure(response.StopReason)
		return turnOutcome{
			kind: turnOutcomeFailed,
			result: state.failure(
				runner,
				runtimeConfig,
				code,
				message,
				nil,
				response.RequestID,
			),
		}
	}

	response.Usage = state.usage
	runner.emit(events.TypeOutput, map[string]any{
		"mediaType": resultv1alpha1.DefaultMediaType,
		"value":     runtimeapi.MessageText(response.Message),
	})
	return turnOutcome{
		kind:     turnOutcomeFinal,
		response: response,
		result: Result{
			Envelope: Success(
				response,
				state.metadata(runtimeConfig, response.RequestID),
			),
			ExitCode: 0,
		},
	}
}

func (runner Runner) executeToolBatch(
	ctx context.Context,
	runtimeConfig config.Config,
	executor ToolExecutor,
	state *loopState,
	outcome turnOutcome,
) *Result {
	requestID := outcome.response.RequestID
	if limitReached(runtimeConfig.TokenBudget, state.consumedTokens) {
		result := state.limitFailure(
			runner,
			runtimeConfig,
			"token_limit_exceeded",
			"run token budget was exhausted before tool results could be returned",
			requestID,
		)
		return &result
	}
	if limitReached(runtimeConfig.MaxTurns, int64(state.turns)) {
		result := state.limitFailure(
			runner,
			runtimeConfig,
			"turn_limit_exceeded",
			"maximum provider turns reached before tool results could be returned",
			requestID,
		)
		return &result
	}
	if batchExceedsLimit(
		runtimeConfig.MaxToolCalls,
		int64(state.toolCalls),
		int64(len(outcome.calls)),
	) {
		result := state.limitFailure(
			runner,
			runtimeConfig,
			"tool_call_limit_exceeded",
			"maximum tool calls reached before final output",
			requestID,
		)
		return &result
	}
	if limitReached(
		runtimeConfig.MaxTotalToolOutputBytes,
		state.totalToolOutputBytes,
	) {
		result := state.limitFailure(
			runner,
			runtimeConfig,
			"tool_output_limit_exceeded",
			"total tool-output limit reached before final output",
			requestID,
		)
		return &result
	}

	resultBlocks := make([]runtimeapi.ContentBlock, 0, len(outcome.calls))
	for _, call := range outcome.calls {
		if limitReached(runtimeConfig.MaxToolCalls, int64(state.toolCalls)) {
			result := state.limitFailure(
				runner,
				runtimeConfig,
				"tool_call_limit_exceeded",
				"maximum tool calls reached before final output",
				requestID,
			)
			return &result
		}
		if limitReached(
			runtimeConfig.MaxTotalToolOutputBytes,
			state.totalToolOutputBytes,
		) {
			result := state.limitFailure(
				runner,
				runtimeConfig,
				"tool_output_limit_exceeded",
				"total tool-output limit reached before final output",
				requestID,
			)
			return &result
		}

		state.toolCalls++
		toolStartedAt := state.now().UTC()
		toolResult, err := executor.Execute(ctx, call)
		if err != nil {
			code, message, retryable, _ := normalizeError(ctx, err)
			result := state.failure(
				runner,
				runtimeConfig,
				code,
				message,
				retryable,
				requestID,
			)
			return &result
		}
		toolResult = applyToolOutputLimits(
			toolResult,
			runtimeConfig,
			&state.totalToolOutputBytes,
		)
		toolEvent := map[string]any{
			"callId":         toolResult.CallID,
			"name":           toolResult.Name,
			"count":          state.toolCalls,
			"durationMillis": state.now().UTC().Sub(toolStartedAt).Milliseconds(),
			"isError":        toolResult.IsError,
			"errorCode":      toolResult.ErrorCode,
			"truncated":      toolResult.Truncated,
			"outputBytes":    len(toolResult.Content),
		}
		if runtimeConfig.EmitToolOutput {
			toolEvent["output"] = toolResult.Content
		}
		runner.emit(events.TypeTool, toolEvent)
		resultCopy := toolResult
		resultBlocks = append(resultBlocks, runtimeapi.ContentBlock{
			Type:       runtimeapi.ContentTypeToolResult,
			ToolResult: &resultCopy,
		})
	}
	state.conversation.Append(runtimeapi.Message{
		Role:    runtimeapi.RoleTool,
		Content: resultBlocks,
	})
	return nil
}
