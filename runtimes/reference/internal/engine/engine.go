package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/MFS-code/Kontext/internal/tooloutput"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/events"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
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
	Close(context.Context) error
}

type ContextToolResolver func(context.Context, config.Config) (ToolExecutor, error)

type Runner struct {
	Emitter             Emitter
	Resolve             Resolver
	ResolveToolsContext ContextToolResolver
	Now                 func() time.Time
}

type Result struct {
	Envelope resultv1alpha1.Envelope
	ExitCode int
}

type execution struct {
	emitter      Emitter
	config       config.Config
	provider     provider.Provider
	toolExecutor ToolExecutor
	state        *loopState
	limits       limitsPolicy
}

func (runner Runner) Run(ctx context.Context, runtimeConfig config.Config) Result {
	resolve := runner.Resolve
	if resolve == nil {
		resolve = provider.Resolve
	}
	now := runner.Now
	if now == nil {
		now = time.Now
	}
	execution := &execution{
		emitter: runner.Emitter,
		config:  runtimeConfig,
		state:   newLoopState(runtimeConfig.Goal, now),
		limits:  newLimitsPolicy(runtimeConfig),
	}

	execution.emit(events.TypeLifecycle, map[string]any{
		"phase":     "started",
		"runName":   runtimeConfig.RunName,
		"agentName": runtimeConfig.AgentName,
		"provider":  runtimeConfig.Provider,
		"model":     runtimeConfig.Model,
	})
	if runner.ResolveToolsContext == nil {
		return execution.fail(
			"invalid_tool_configuration",
			"tool resolver is required",
			nil,
			"",
		)
	}
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
		return execution.fail(code, err.Error(), nil, "")
	}
	execution.provider = selectedProvider
	toolExecutor, err := runner.ResolveToolsContext(ctx, runtimeConfig)
	execution.toolExecutor = toolExecutor
	if err != nil {
		code := "invalid_tool_configuration"
		var toolError *runtimeapi.CodedError
		if errors.As(err, &toolError) && toolError.Code != "" {
			code = toolError.Code
		}
		return execution.finish(execution.fail(code, err.Error(), nil, ""))
	}
	if toolExecutor == nil {
		return execution.fail(
			"invalid_tool_configuration",
			"tool resolver returned a nil tool executor",
			nil,
			"",
		)
	}
	return execution.run(ctx)
}

func (execution *execution) run(ctx context.Context) Result {
	definitions := execution.toolExecutor.Definitions()
	if len(definitions) > 0 {
		names := make([]string, 0, len(definitions))
		for _, definition := range definitions {
			names = append(names, definition.Name)
		}
		execution.emit(events.TypeLifecycle, map[string]any{
			"phase": "tools_available",
			"count": len(definitions),
			"names": names,
		})
	}

	for {
		outcome := execution.providerTurn(ctx, definitions)
		switch outcome.kind {
		case turnOutcomePaused:
			continue
		case turnOutcomeFinal:
			result := execution.finish(outcome.result)
			if result.Envelope.Output != nil {
				execution.emit(events.TypeOutput, map[string]any{
					"mediaType": resultv1alpha1.DefaultMediaType,
					"value":     runtimeapi.MessageText(outcome.response.Message),
				})
			}
			return result
		case turnOutcomeFailed:
			return execution.finish(outcome.result)
		case turnOutcomeToolBatch:
			if result := execution.executeToolBatch(ctx, outcome); result != nil {
				return execution.finish(*result)
			}
		default:
			panic(fmt.Sprintf("unsupported turn outcome %d", outcome.kind))
		}
	}
}

func (execution *execution) finish(result Result) Result {
	if execution.toolExecutor == nil {
		return result
	}
	return execution.cleanupToolExecutor(result)
}

func (execution *execution) cleanupToolExecutor(result Result) Result {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := execution.toolExecutor.Close(cleanupCtx); err != nil {
		const code = "tool_cleanup_failed"
		message := tooloutput.TruncateUTF8(fmt.Sprintf("tool cleanup failed: %v", err), 4<<10)
		if result.ExitCode == 0 {
			failure := execution.fail(code, message, nil, execution.state.lastRequestID)
			failure.Envelope.Output = result.Envelope.Output
			return failure
		}
		execution.emitError(code, message, nil)
	}
	return result
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

type limitViolation struct {
	code    string
	message string
}

type limitsPolicy struct {
	tokenBudget             *int64
	maxTurns                *int64
	maxToolCalls            *int64
	maxToolResultBytes      *int64
	maxTotalToolOutputBytes *int64
}

func newLimitsPolicy(runtimeConfig config.Config) limitsPolicy {
	return limitsPolicy{
		tokenBudget:             runtimeConfig.TokenBudget,
		maxTurns:                runtimeConfig.MaxTurns,
		maxToolCalls:            runtimeConfig.MaxToolCalls,
		maxToolResultBytes:      runtimeConfig.MaxToolResultBytes,
		maxTotalToolOutputBytes: runtimeConfig.MaxTotalToolOutputBytes,
	}
}

func (limits limitsPolicy) checkBeforeProviderTurn(state *loopState) *limitViolation {
	if limits.reached(limits.maxTurns, int64(state.turns)) {
		return &limitViolation{
			code:    "turn_limit_exceeded",
			message: "maximum provider turns reached before final output",
		}
	}
	return nil
}

func (limits limitsPolicy) checkAfterResponse(state *loopState) *limitViolation {
	// Provider usage is checked after a response and may equal the budget. A
	// follow-up tool batch requires remaining budget, so its preflight uses >=.
	if limits.tokenBudget != nil && state.consumedTokens > *limits.tokenBudget {
		return &limitViolation{
			code:    "token_limit_exceeded",
			message: "measured provider usage exceeded the run token budget",
		}
	}
	return nil
}

func (limits limitsPolicy) checkBeforeToolBatch(
	state *loopState,
	requestedCalls int64,
) *limitViolation {
	if limits.reached(limits.tokenBudget, state.consumedTokens) {
		return &limitViolation{
			code:    "token_limit_exceeded",
			message: "run token budget was exhausted before tool results could be returned",
		}
	}
	if limits.reached(limits.maxTurns, int64(state.turns)) {
		return &limitViolation{
			code:    "turn_limit_exceeded",
			message: "maximum provider turns reached before tool results could be returned",
		}
	}
	if limits.batchExceeds(limits.maxToolCalls, int64(state.toolCalls), requestedCalls) {
		return &limitViolation{
			code:    "tool_call_limit_exceeded",
			message: "maximum tool calls reached before final output",
		}
	}
	return limits.checkBeforeToolCall(state)
}

func (limits limitsPolicy) checkBeforeToolCall(state *loopState) *limitViolation {
	if limits.reached(limits.maxTotalToolOutputBytes, state.totalToolOutputBytes) {
		return &limitViolation{
			code:    "tool_output_limit_exceeded",
			message: "total tool-output limit reached before final output",
		}
	}
	return nil
}

func (limits limitsPolicy) remainingTokenBudget(consumed int64) *int64 {
	if limits.tokenBudget == nil {
		return nil
	}
	remaining := *limits.tokenBudget - consumed
	if remaining < 1 {
		remaining = 1
	}
	return &remaining
}

func (limits limitsPolicy) applyToolOutput(
	result runtimeapi.ToolResult,
	totalBytes *int64,
) runtimeapi.ToolResult {
	maxBytes := int64(len(result.Content))
	if limits.maxToolResultBytes != nil && *limits.maxToolResultBytes < maxBytes {
		maxBytes = *limits.maxToolResultBytes
	}
	if limits.maxTotalToolOutputBytes != nil {
		remaining := *limits.maxTotalToolOutputBytes - *totalBytes
		if remaining < maxBytes {
			maxBytes = remaining
		}
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	if content, truncated := tooloutput.Bound(result.Content, maxBytes); truncated {
		result.Content = content
		result.Truncated = true
	}
	*totalBytes += int64(len(result.Content))
	return result
}

func (limits limitsPolicy) reached(limit *int64, value int64) bool {
	return limit != nil && value >= *limit
}

func (limits limitsPolicy) batchExceeds(
	limit *int64,
	consumed int64,
	requested int64,
) bool {
	if limit == nil {
		return false
	}
	remaining := *limit - consumed
	return remaining < 0 || requested > remaining
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
	total.ReasoningTokens = addMetric(total.ReasoningTokens, current.ReasoningTokens)
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

func (execution *execution) fail(
	code string,
	message string,
	retryable *bool,
	requestID string,
) Result {
	execution.emitError(code, message, retryable)
	result := Result{
		Envelope: failureEnvelope(
			code,
			message,
			retryable,
			execution.state.metadata(execution.config, requestID),
		),
		ExitCode: 1,
	}
	result.Envelope.Usage = envelopeUsage(execution.state.usage)
	return result
}

func (execution *execution) failLimit(
	violation *limitViolation,
	requestID string,
) Result {
	retryable := false
	return execution.fail(violation.code, violation.message, &retryable, requestID)
}

func (execution *execution) emit(eventType events.Type, data any) {
	if execution.emitter == nil {
		return
	}
	execution.emitter.Emit(eventType, data)
}

func (execution *execution) emitError(code string, message string, retryable *bool) {
	execution.emit(events.TypeError, map[string]any{
		"code":      code,
		"message":   message,
		"retryable": retryable,
	})
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
