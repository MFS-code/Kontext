package provider

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

const (
	FakeScenarioSuccess   = "success"
	FakeScenarioFailure   = "failure"
	FakeScenarioMalformed = "malformed"
	FakeScenarioDelay     = "delay"
)

type Provider interface {
	Name() string
	Complete(context.Context, runtimeapi.CompletionRequest) (runtimeapi.CompletionResponse, error)
}

type UnsupportedError struct {
	Name string
}

func (err *UnsupportedError) Error() string {
	return fmt.Sprintf("unsupported provider %q", err.Name)
}

type ConfigurationError struct {
	Provider string
	Code     string
	Message  string
}

func (err *ConfigurationError) Error() string {
	if err.Provider == "" {
		return fmt.Sprintf("provider configuration is invalid: %s", err.Message)
	}
	return fmt.Sprintf("%s provider configuration is invalid: %s", err.Provider, err.Message)
}

func Resolve(runtimeConfig config.Config) (Provider, error) {
	switch runtimeConfig.Provider {
	case "fake":
		return resolveFake(runtimeConfig)
	case "anthropic":
		return NewAnthropic(AnthropicConfig{
			APIKey:   runtimeConfig.AnthropicAPIKey,
			Endpoint: runtimeConfig.ProviderEndpoint,
			BaseURL:  runtimeConfig.ProviderBaseURL,
			Client:   http.DefaultClient,
		})
	case "openai", "openai-compatible":
		return NewOpenAI(OpenAIConfig{
			Name:     runtimeConfig.Provider,
			APIKey:   runtimeConfig.OpenAIAPIKey,
			Endpoint: runtimeConfig.ProviderEndpoint,
			BaseURL:  runtimeConfig.ProviderBaseURL,
			Client:   http.DefaultClient,
		})
	default:
		return nil, &UnsupportedError{Name: runtimeConfig.Provider}
	}
}

func resolveFake(runtimeConfig config.Config) (Provider, error) {
	scenario := runtimeConfig.FakeScenario
	if scenario == "" {
		scenario = FakeScenarioSuccess
	}
	switch scenario {
	case FakeScenarioSuccess, FakeScenarioFailure, FakeScenarioMalformed:
	case FakeScenarioDelay:
		if runtimeConfig.FakeDelay <= 0 {
			return nil, &ConfigurationError{
				Provider: "fake",
				Message:  "KONTEXT_FAKE_DELAY is required for delay scenario",
			}
		}
	default:
		return nil, &ConfigurationError{
			Provider: "fake",
			Message:  fmt.Sprintf("unsupported KONTEXT_FAKE_SCENARIO %q", scenario),
		}
	}
	return &Fake{
		Scenario: scenario,
		Delay:    runtimeConfig.FakeDelay,
	}, nil
}

type Fake struct {
	Scenario string
	Delay    time.Duration
}

func (fake *Fake) Name() string {
	return "fake"
}

func (fake *Fake) Complete(
	ctx context.Context,
	request runtimeapi.CompletionRequest,
) (runtimeapi.CompletionResponse, error) {
	select {
	case <-ctx.Done():
		return runtimeapi.CompletionResponse{}, ctx.Err()
	default:
	}

	switch fake.Scenario {
	case FakeScenarioFailure:
		retryable := false
		return runtimeapi.CompletionResponse{}, &runtimeapi.ProviderError{
			Code:      "fake_provider_failure",
			Message:   "fake provider failed as configured",
			Retryable: &retryable,
		}
	case FakeScenarioMalformed:
		return runtimeapi.CompletionResponse{
			Message: runtimeapi.Message{
				Role: runtimeapi.Role("malformed"),
				Content: []runtimeapi.ContentBlock{
					{Type: runtimeapi.ContentTypeText, Text: "invalid response"},
				},
			},
			StopReason: runtimeapi.StopReasonEndTurn,
		}, nil
	case FakeScenarioDelay:
		timer := time.NewTimer(fake.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return runtimeapi.CompletionResponse{}, ctx.Err()
		case <-timer.C:
		}
	case FakeScenarioSuccess:
	default:
		return runtimeapi.CompletionResponse{}, fmt.Errorf("unsupported fake scenario %q", fake.Scenario)
	}

	return fakeSuccess(request), nil
}

func fakeSuccess(request runtimeapi.CompletionRequest) runtimeapi.CompletionResponse {
	goal := ""
	if len(request.Messages) > 0 {
		goal = runtimeapi.MessageText(request.Messages[len(request.Messages)-1])
	}
	output := fmt.Sprintf("Fake provider completed goal: %s", goal)
	inputTokens := int64(len(strings.Fields(goal)))
	outputTokens := int64(len(strings.Fields(output)))
	totalTokens := inputTokens + outputTokens
	requestHash := sha256.Sum256([]byte(request.Model + "\x00" + goal))

	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{Type: runtimeapi.ContentTypeText, Text: output},
			},
		},
		Usage: runtimeapi.Usage{
			InputTokens:  &inputTokens,
			OutputTokens: &outputTokens,
			TotalTokens:  &totalTokens,
		},
		StopReason: runtimeapi.StopReasonEndTurn,
		RequestID:  fmt.Sprintf("fake-%x", requestHash[:6]),
	}
}
