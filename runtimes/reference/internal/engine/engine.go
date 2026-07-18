package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/conversation"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/events"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

// Emitter publishes best-effort observability events. The result envelope is
// the runtime contract; event emission never fails a run.
type Emitter interface {
	Emit(events.Type, any)
}

type Resolver func(config.Config) (provider.Provider, error)

type Runner struct {
	Emitter Emitter
	Resolve Resolver
	Now     func() time.Time
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
	now := runner.Now
	if now == nil {
		now = time.Now
	}
	startedAt := now().UTC()
	metadata := func(requestID string) Metadata {
		return Metadata{
			Provider:    runtimeConfig.Provider,
			Model:       runtimeConfig.Model,
			RequestID:   requestID,
			StartedAt:   startedAt,
			CompletedAt: now().UTC(),
		}
	}

	runner.emit(events.TypeLifecycle, map[string]any{
		"phase":     "started",
		"runName":   runtimeConfig.RunName,
		"agentName": runtimeConfig.AgentName,
		"provider":  runtimeConfig.Provider,
		"model":     runtimeConfig.Model,
	})
	if len(runtimeConfig.Tools) > 0 {
		runner.emit(events.TypeLifecycle, map[string]any{
			"phase": "tools_declared_not_executed",
			"count": len(runtimeConfig.Tools),
		})
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
		runner.emitError(code, err.Error(), nil)
		return failed(code, err.Error(), nil, metadata(""))
	}

	state := conversation.New(runtimeConfig.Goal)
	request := runtimeapi.CompletionRequest{
		Model:     runtimeConfig.Model,
		Messages:  state.Messages(),
		MaxTokens: runtimeConfig.TokenBudget,
	}

	response, err := selectedProvider.Complete(ctx, request)
	if err != nil {
		code, message, retryable, requestID := normalizeError(ctx, err)
		runner.emitError(code, message, retryable)
		return failed(code, message, retryable, metadata(requestID))
	}
	if err := runtimeapi.ValidateResponse(response); err != nil {
		message := fmt.Sprintf("provider returned an invalid response: %v", err)
		runner.emitError("invalid_provider_response", message, nil)
		return failed(
			"invalid_provider_response",
			message,
			nil,
			metadata(response.RequestID),
		)
	}

	state.Append(response.Message)
	completedMetadata := metadata(response.RequestID)
	runner.emit(events.TypeLifecycle, map[string]any{
		"phase":          "provider_completed",
		"stopReason":     response.StopReason,
		"durationMillis": completedMetadata.CompletedAt.Sub(startedAt).Milliseconds(),
	})
	if usage := envelopeUsage(response.Usage); usage != nil {
		runner.emit(events.TypeUsage, usage)
	}
	for _, toolCall := range runtimeapi.MessageToolCalls(response.Message) {
		runner.emit(events.TypeTool, map[string]any{
			"id":        toolCall.ID,
			"name":      toolCall.Name,
			"arguments": toolCall.Arguments,
			"executed":  false,
		})
	}
	runner.emit(events.TypeOutput, map[string]any{
		"mediaType": resultv1alpha1.DefaultMediaType,
		"value":     runtimeapi.MessageText(response.Message),
	})

	return Result{
		Envelope: Success(response, completedMetadata),
		ExitCode: 0,
	}
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
