package engine

import (
	"context"
	"errors"
	"fmt"
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
