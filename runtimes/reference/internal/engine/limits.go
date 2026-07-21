package engine

import (
	"math"

	"github.com/MFS-code/Kontext/internal/tooloutput"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

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
