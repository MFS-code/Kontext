package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/conversation"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/events"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/tools"
)

// Emitter publishes best-effort observability events. The result envelope is
// the runtime contract; event emission never fails a run.
type Emitter interface {
	Emit(events.Type, any)
}

type Resolver func(config.Config) (provider.Provider, error)

type ToolExecutor interface {
	Definitions() []runtimeapi.ToolDefinition
	Execute(context.Context, runtimeapi.ToolCall) (runtimeapi.ToolResult, error)
}

type ToolResolver func(config.Config) (ToolExecutor, error)

type Runner struct {
	Emitter      Emitter
	Resolve      Resolver
	ResolveTools ToolResolver
	Now          func() time.Time
}

type Result struct {
	Envelope resultv1alpha1.Envelope
	ExitCode int
}

func (runner Runner) Run(ctx context.Context, runtimeConfig config.Config) Result {
	resolve := runner.Resolve
	if resolve == nil {
		resolve = provider.Resolve
	}
	resolveTools := runner.ResolveTools
	if resolveTools == nil {
		resolveTools = func(runtimeConfig config.Config) (ToolExecutor, error) {
			return tools.New(tools.Config{
				Allowed: runtimeConfig.Tools,
			})
		}
	}
	now := runner.Now
	if now == nil {
		now = time.Now
	}
	startedAt := now().UTC()
	var turns int32
	var toolCalls int32
	metadata := func(requestID string) Metadata {
		return Metadata{
			Provider:    runtimeConfig.Provider,
			Model:       runtimeConfig.Model,
			RequestID:   requestID,
			StartedAt:   startedAt,
			CompletedAt: now().UTC(),
			Turns:       turns,
			ToolCalls:   toolCalls,
		}
	}

	runner.emit(events.TypeLifecycle, map[string]any{
		"phase":     "started",
		"runName":   runtimeConfig.RunName,
		"agentName": runtimeConfig.AgentName,
		"provider":  runtimeConfig.Provider,
		"model":     runtimeConfig.Model,
	})
	selectedProvider, err := resolve(runtimeConfig)
	if err != nil {
		code := "unsupported_provider"
		var configurationError *provider.ConfigurationError
		if errors.As(err, &configurationError) {
			code = configurationError.Code
			if code == "" {
				code = "invalid_provider_configuration"
			}
		}
		runner.emitError(code, err.Error(), nil)
		return failed(code, err.Error(), nil, metadata(""))
	}
	toolExecutor, err := resolveTools(runtimeConfig)
	if err != nil {
		code := "invalid_tool_configuration"
		var toolError *tools.Error
		if errors.As(err, &toolError) && toolError.Code != "" {
			code = toolError.Code
		}
		runner.emitError(code, err.Error(), nil)
		return failed(code, err.Error(), nil, metadata(""))
	}
	definitions := toolExecutor.Definitions()
	if len(definitions) > 0 {
		names := make([]string, 0, len(definitions))
		for _, definition := range definitions {
			names = append(names, definition.Name)
		}
		runner.emit(events.TypeLifecycle, map[string]any{
			"phase": "tools_available",
			"count": len(definitions),
			"names": names,
		})
	}

	state := conversation.New(runtimeConfig.Goal)
	var totalUsage runtimeapi.Usage
	var consumedTokens int64
	var totalToolOutputBytes int64
	lastRequestID := ""

	for {
		if limitReached(runtimeConfig.MaxTurns, int64(turns)) {
			return runner.limitFailure(
				"turn_limit_exceeded",
				"maximum provider turns reached before final output",
				totalUsage,
				metadata(lastRequestID),
			)
		}
		requestStartedAt := now().UTC()
		turns++
		request := runtimeapi.CompletionRequest{
			Model:     runtimeConfig.Model,
			Messages:  state.Messages(),
			MaxTokens: remainingTokenBudget(runtimeConfig.TokenBudget, consumedTokens),
			Tools:     runtimeapi.CloneToolDefinitions(definitions),
		}
		response, err := selectedProvider.Complete(ctx, request)
		if err != nil {
			code, message, retryable, requestID := normalizeError(ctx, err)
			runner.emitError(code, message, retryable)
			result := failed(code, message, retryable, metadata(requestID))
			result.Envelope.Usage = envelopeUsage(totalUsage)
			return result
		}
		lastRequestID = response.RequestID
		if err := runtimeapi.ValidateResponse(response); err != nil {
			message := fmt.Sprintf("provider returned an invalid response: %v", err)
			runner.emitError("invalid_provider_response", message, nil)
			result := failed(
				"invalid_provider_response",
				message,
				nil,
				metadata(response.RequestID),
			)
			result.Envelope.Usage = envelopeUsage(totalUsage)
			return result
		}

		totalUsage = addUsage(totalUsage, response.Usage)
		consumedTokens = saturatingAdd(consumedTokens, measuredTokens(response.Usage))
		runner.emit(events.TypeLifecycle, map[string]any{
			"phase":          "provider_completed",
			"turn":           turns,
			"stopReason":     response.StopReason,
			"durationMillis": now().UTC().Sub(requestStartedAt).Milliseconds(),
		})
		if usage := envelopeUsage(response.Usage); usage != nil {
			runner.emit(events.TypeUsage, map[string]any{
				"turn":  turns,
				"usage": usage,
			})
		}
		if tokenBudgetExceeded(runtimeConfig.TokenBudget, consumedTokens) {
			return runner.limitFailure(
				"token_limit_exceeded",
				"measured provider usage exceeded the run token budget",
				totalUsage,
				metadata(response.RequestID),
			)
		}

		state.Append(response.Message)
		calls := runtimeapi.MessageToolCalls(response.Message)
		if len(calls) == 0 {
			switch response.StopReason {
			case runtimeapi.StopReasonPauseTurn:
				continue
			case runtimeapi.StopReasonEndTurn, runtimeapi.StopReasonStopSequence:
			default:
				code, message := terminalStopFailure(response.StopReason)
				runner.emitError(code, message, nil)
				result := failed(code, message, nil, metadata(response.RequestID))
				result.Envelope.Usage = envelopeUsage(totalUsage)
				return result
			}
			response.Usage = totalUsage
			completedMetadata := metadata(response.RequestID)
			runner.emit(events.TypeOutput, map[string]any{
				"mediaType": resultv1alpha1.DefaultMediaType,
				"value":     runtimeapi.MessageText(response.Message),
			})
			return Result{
				Envelope: Success(response, completedMetadata),
				ExitCode: 0,
			}
		}
		if response.StopReason != runtimeapi.StopReasonToolUse {
			message := fmt.Sprintf(
				"provider returned tool calls with stop reason %q",
				response.StopReason,
			)
			runner.emitError("invalid_provider_response", message, nil)
			result := failed(
				"invalid_provider_response",
				message,
				nil,
				metadata(response.RequestID),
			)
			result.Envelope.Usage = envelopeUsage(totalUsage)
			return result
		}
		if limitReached(runtimeConfig.TokenBudget, consumedTokens) {
			return runner.limitFailure(
				"token_limit_exceeded",
				"run token budget was exhausted before tool results could be returned",
				totalUsage,
				metadata(response.RequestID),
			)
		}
		if limitReached(runtimeConfig.MaxTurns, int64(turns)) {
			return runner.limitFailure(
				"turn_limit_exceeded",
				"maximum provider turns reached before tool results could be returned",
				totalUsage,
				metadata(response.RequestID),
			)
		}

		if batchExceedsLimit(
			runtimeConfig.MaxToolCalls,
			int64(toolCalls),
			int64(len(calls)),
		) {
			return runner.limitFailure(
				"tool_call_limit_exceeded",
				"maximum tool calls reached before final output",
				totalUsage,
				metadata(response.RequestID),
			)
		}
		if limitReached(
			runtimeConfig.MaxTotalToolOutputBytes,
			totalToolOutputBytes,
		) {
			return runner.limitFailure(
				"tool_output_limit_exceeded",
				"total tool-output limit reached before final output",
				totalUsage,
				metadata(response.RequestID),
			)
		}

		resultBlocks := make([]runtimeapi.ContentBlock, 0, len(calls))
		for _, call := range calls {
			if limitReached(runtimeConfig.MaxToolCalls, int64(toolCalls)) {
				return runner.limitFailure(
					"tool_call_limit_exceeded",
					"maximum tool calls reached before final output",
					totalUsage,
					metadata(response.RequestID),
				)
			}
			if limitReached(
				runtimeConfig.MaxTotalToolOutputBytes,
				totalToolOutputBytes,
			) {
				return runner.limitFailure(
					"tool_output_limit_exceeded",
					"total tool-output limit reached before final output",
					totalUsage,
					metadata(response.RequestID),
				)
			}
			toolCalls++
			toolStartedAt := now().UTC()
			toolResult, err := toolExecutor.Execute(ctx, call)
			if err != nil {
				code, message, retryable, _ := normalizeError(ctx, err)
				runner.emitError(code, message, retryable)
				result := failed(code, message, retryable, metadata(response.RequestID))
				result.Envelope.Usage = envelopeUsage(totalUsage)
				return result
			}
			toolResult = applyToolOutputLimits(
				toolResult,
				runtimeConfig,
				&totalToolOutputBytes,
			)
			toolEvent := map[string]any{
				"callId":         toolResult.CallID,
				"name":           toolResult.Name,
				"count":          toolCalls,
				"durationMillis": now().UTC().Sub(toolStartedAt).Milliseconds(),
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
		state.Append(runtimeapi.Message{
			Role:    runtimeapi.RoleTool,
			Content: resultBlocks,
		})
	}
}

func terminalStopFailure(reason runtimeapi.StopReason) (string, string) {
	switch reason {
	case runtimeapi.StopReasonMaxTokens:
		return "max_tokens_reached", "provider stopped before final output after reaching its token limit"
	case runtimeapi.StopReasonContentFilter:
		return "content_filtered", "provider filtered the response before final output"
	case runtimeapi.StopReasonRefusal:
		return "provider_refusal", "provider refused the request"
	case runtimeapi.StopReasonModelContextWindowExceeded:
		return "context_window_exceeded", "provider context window was exhausted before final output"
	case runtimeapi.StopReasonToolUse:
		return "invalid_provider_response", "provider stopped for tool use without returning a tool call"
	default:
		return "invalid_provider_response", fmt.Sprintf(
			"provider stopped without final output for reason %q",
			reason,
		)
	}
}

func limitReached(limit *int64, value int64) bool {
	return limit != nil && value >= *limit
}

func batchExceedsLimit(limit *int64, consumed int64, requested int64) bool {
	if limit == nil {
		return false
	}
	remaining := *limit - consumed
	return remaining < 0 || requested > remaining
}

func remainingTokenBudget(limit *int64, consumed int64) *int64 {
	if limit == nil {
		return nil
	}
	remaining := *limit - consumed
	if remaining < 1 {
		remaining = 1
	}
	return &remaining
}

func tokenBudgetExceeded(limit *int64, consumed int64) bool {
	return limit != nil && consumed > *limit
}

func measuredTokens(usage runtimeapi.Usage) int64 {
	var measuredParts int64
	if usage.InputTokens != nil {
		measuredParts = saturatingAdd(measuredParts, *usage.InputTokens)
	}
	if usage.OutputTokens != nil {
		measuredParts = saturatingAdd(measuredParts, *usage.OutputTokens)
	}
	if usage.TotalTokens != nil && *usage.TotalTokens > measuredParts {
		return *usage.TotalTokens
	}
	return measuredParts
}

func addUsage(total runtimeapi.Usage, current runtimeapi.Usage) runtimeapi.Usage {
	total.InputTokens = addMetric(total.InputTokens, current.InputTokens)
	total.OutputTokens = addMetric(total.OutputTokens, current.OutputTokens)
	total.TotalTokens = addMetric(total.TotalTokens, current.TotalTokens)
	return total
}

func addMetric(total *int64, current *int64) *int64 {
	if current == nil {
		return total
	}
	if total == nil {
		value := *current
		return &value
	}
	value := saturatingAdd(*total, *current)
	return &value
}

func saturatingAdd(left int64, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func applyToolOutputLimits(
	result runtimeapi.ToolResult,
	runtimeConfig config.Config,
	totalBytes *int64,
) runtimeapi.ToolResult {
	maxBytes := int64(len(result.Content))
	if runtimeConfig.MaxToolResultBytes != nil &&
		*runtimeConfig.MaxToolResultBytes < maxBytes {
		maxBytes = *runtimeConfig.MaxToolResultBytes
	}
	if runtimeConfig.MaxTotalToolOutputBytes != nil {
		remaining := *runtimeConfig.MaxTotalToolOutputBytes - *totalBytes
		if remaining < maxBytes {
			maxBytes = remaining
		}
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	if int64(len(result.Content)) > maxBytes {
		result.Content = truncateToolContent(result.Content, maxBytes)
		result.Truncated = true
	}
	*totalBytes += int64(len(result.Content))
	return result
}

func truncateToolContent(value string, maxBytes int64) string {
	if !json.Valid([]byte(value)) {
		return truncateUTF8(value, maxBytes)
	}
	if maxBytes <= 0 {
		return ""
	}
	if maxBytes == 1 {
		return "0"
	}

	const emptyPartial = `{"partial":""}`
	if maxBytes < int64(len(emptyPartial)) {
		return "{}"
	}

	low := 0
	high := len(value)
	best := emptyPartial
	for low <= high {
		middle := low + (high-low)/2
		prefix := truncateUTF8(value, int64(middle))
		encoded, _ := json.Marshal(struct {
			Partial string `json:"partial"`
		}{Partial: prefix})
		if int64(len(encoded)) <= maxBytes {
			best = string(encoded)
			low = middle + 1
			continue
		}
		high = middle - 1
	}
	return best
}

func truncateUTF8(value string, maxBytes int64) string {
	if maxBytes <= 0 {
		return ""
	}
	if int64(len(value)) <= maxBytes {
		return value
	}
	end := int(maxBytes)
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func (runner Runner) limitFailure(
	code string,
	message string,
	usage runtimeapi.Usage,
	metadata Metadata,
) Result {
	retryable := false
	runner.emitError(code, message, &retryable)
	result := failed(code, message, &retryable, metadata)
	result.Envelope.Usage = envelopeUsage(usage)
	return result
}

func (runner Runner) emit(eventType events.Type, data any) {
	if runner.Emitter == nil {
		return
	}
	runner.Emitter.Emit(eventType, data)
}

func (runner Runner) emitError(code string, message string, retryable *bool) {
	runner.emit(events.TypeError, map[string]any{
		"code":      code,
		"message":   message,
		"retryable": retryable,
	})
}

func failed(
	code string,
	message string,
	retryable *bool,
	metadata Metadata,
) Result {
	return Result{
		Envelope: Failure(code, message, retryable, metadata),
		ExitCode: 1,
	}
}

func normalizeError(
	ctx context.Context,
	err error,
) (code string, message string, retryable *bool, requestID string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		value := true
		return "deadline_exceeded", "runtime wallclock limit exceeded", &value, ""
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		value := true
		return "cancelled", "runtime execution was cancelled", &value, ""
	}
	var providerError *runtimeapi.ProviderError
	if errors.As(err, &providerError) {
		return providerError.Code,
			providerError.Message,
			providerError.Retryable,
			providerError.RequestID
	}
	var configurationError *provider.ConfigurationError
	if errors.As(err, &configurationError) {
		code := configurationError.Code
		if code == "" {
			code = "invalid_provider_configuration"
		}
		return code, configurationError.Error(), nil, ""
	}
	return "provider_error", err.Error(), nil, ""
}
