package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/conversation"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/events"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
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

func (execution *execution) providerTurn(
	ctx context.Context,
	definitions []runtimeapi.ToolDefinition,
) turnOutcome {
	state := execution.state
	if violation := execution.limits.checkBeforeProviderTurn(state); violation != nil {
		return turnOutcome{
			kind:   turnOutcomeFailed,
			result: execution.failLimit(violation, state.lastRequestID),
		}
	}

	requestStartedAt := state.now().UTC()
	state.turns++
	response, err := execution.provider.Complete(ctx, runtimeapi.CompletionRequest{
		Model:     execution.config.Model,
		Messages:  state.conversation.Messages(),
		MaxTokens: execution.limits.remainingTokenBudget(state.consumedTokens),
		Tools:     runtimeapi.CloneToolDefinitions(definitions),
	})
	if err != nil {
		code, message, retryable, requestID := normalizeError(ctx, err)
		return turnOutcome{
			kind:   turnOutcomeFailed,
			result: execution.fail(code, message, retryable, requestID),
		}
	}

	state.lastRequestID = response.RequestID
	if err := runtimeapi.ValidateResponse(response); err != nil {
		message := fmt.Sprintf("provider returned an invalid response: %v", err)
		return turnOutcome{
			kind: turnOutcomeFailed,
			result: execution.fail(
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
	execution.emit(events.TypeLifecycle, map[string]any{
		"phase":          "provider_completed",
		"turn":           state.turns,
		"stopReason":     response.StopReason,
		"durationMillis": state.now().UTC().Sub(requestStartedAt).Milliseconds(),
	})
	if usage := envelopeUsage(response.Usage); usage != nil {
		execution.emit(events.TypeUsage, map[string]any{
			"turn":  state.turns,
			"usage": usage,
		})
	}
	if violation := execution.limits.checkAfterResponse(state); violation != nil {
		return turnOutcome{
			kind:   turnOutcomeFailed,
			result: execution.failLimit(violation, response.RequestID),
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
				result: execution.fail(
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
			kind:   turnOutcomeFailed,
			result: execution.fail(code, message, nil, response.RequestID),
		}
	}

	return turnOutcome{
		kind:     turnOutcomeFinal,
		response: response,
		result: Result{
			Envelope: Success(
				response.Message,
				state.usage,
				state.metadata(execution.config, response.RequestID),
			),
			ExitCode: 0,
		},
	}
}

func (execution *execution) executeToolBatch(
	ctx context.Context,
	outcome turnOutcome,
) *Result {
	state := execution.state
	requestID := outcome.response.RequestID
	if violation := execution.limits.checkBeforeToolBatch(
		state,
		int64(len(outcome.calls)),
	); violation != nil {
		result := execution.failLimit(violation, requestID)
		return &result
	}

	resultBlocks := make([]runtimeapi.ContentBlock, 0, len(outcome.calls))
	for _, call := range outcome.calls {
		// Output from an earlier call in this batch can consume the remaining
		// byte budget, so this per-call check is reachable.
		if violation := execution.limits.checkBeforeToolCall(state); violation != nil {
			result := execution.failLimit(violation, requestID)
			return &result
		}

		state.toolCalls++
		toolStartedAt := state.now().UTC()
		toolResult, err := execution.toolExecutor.Execute(ctx, call)
		if err != nil {
			code, message, retryable, _ := normalizeError(ctx, err)
			result := execution.fail(code, message, retryable, requestID)
			return &result
		}
		toolResult = execution.limits.applyToolOutput(
			toolResult,
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
		if execution.config.EmitToolOutput {
			toolEvent["output"] = toolResult.Content
		}
		execution.emit(events.TypeTool, toolEvent)
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
