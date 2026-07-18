package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	FakeScenarioTool      = "tool"
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
	case FakeScenarioTool:
		if strings.TrimSpace(runtimeConfig.FakeToolName) == "" {
			return nil, &ConfigurationError{
				Provider: "fake",
				Message:  "KONTEXT_FAKE_TOOL_NAME is required for tool scenario",
			}
		}
		if runtimeConfig.FakeToolArguments == "" {
			runtimeConfig.FakeToolArguments = "{}"
		}
		if !json.Valid([]byte(runtimeConfig.FakeToolArguments)) {
			return nil, &ConfigurationError{
				Provider: "fake",
				Message:  "KONTEXT_FAKE_TOOL_ARGUMENTS must contain valid JSON",
			}
		}
	default:
		return nil, &ConfigurationError{
			Provider: "fake",
			Message:  fmt.Sprintf("unsupported KONTEXT_FAKE_SCENARIO %q", scenario),
		}
	}
	return &Fake{
		Scenario:      scenario,
		Delay:         runtimeConfig.FakeDelay,
		ToolName:      runtimeConfig.FakeToolName,
		ToolArguments: json.RawMessage(runtimeConfig.FakeToolArguments),
	}, nil
}

type Fake struct {
	Scenario      string
	Delay         time.Duration
	ToolName      string
	ToolArguments json.RawMessage
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
	case FakeScenarioTool:
		return fakeToolCompletion(request, fake.ToolName, fake.ToolArguments)
	default:
		return runtimeapi.CompletionResponse{}, fmt.Errorf("unsupported fake scenario %q", fake.Scenario)
	}

	return fakeSuccess(request), nil
}

func fakeToolCompletion(
	request runtimeapi.CompletionRequest,
	toolName string,
	arguments json.RawMessage,
) (runtimeapi.CompletionResponse, error) {
	for _, message := range request.Messages {
		for _, result := range runtimeapi.MessageToolResults(message) {
			output := fmt.Sprintf(
				"Fake provider received %s result: %s",
				result.Name,
				result.Content,
			)
			return fakeTextResponse(
				request.Model,
				output,
				int64(len(strings.Fields(fakeInputText(request.Messages)))),
			), nil
		}
	}
	exposed := false
	for _, definition := range request.Tools {
		if definition.Name == toolName {
			exposed = true
			break
		}
	}
	if !exposed {
		retryable := false
		return runtimeapi.CompletionResponse{}, &runtimeapi.ProviderError{
			Code:      "fake_tool_unavailable",
			Message:   fmt.Sprintf("fake tool %q was not exposed", toolName),
			Retryable: &retryable,
		}
	}
	requestHash := sha256.Sum256([]byte(request.Model + "\x00" + toolName))
	inputTokens := int64(len(strings.Fields(fakeInputText(request.Messages))))
	outputTokens := int64(len(strings.Fields(toolName + " " + string(arguments))))
	totalTokens := inputTokens + outputTokens
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{
					Type: runtimeapi.ContentTypeToolCall,
					ToolCall: &runtimeapi.ToolCall{
						ID:        "fake-tool-call",
						Name:      toolName,
						Arguments: append(json.RawMessage(nil), arguments...),
					},
				},
			},
		},
		Usage: runtimeapi.Usage{
			InputTokens:  &inputTokens,
			OutputTokens: &outputTokens,
			TotalTokens:  &totalTokens,
		},
		StopReason: runtimeapi.StopReasonToolUse,
		RequestID:  fmt.Sprintf("fake-%x", requestHash[:6]),
	}, nil
}

func fakeTextResponse(
	model string,
	output string,
	inputTokens int64,
) runtimeapi.CompletionResponse {
	outputTokens := int64(len(strings.Fields(output)))
	totalTokens := inputTokens + outputTokens
	requestHash := sha256.Sum256([]byte(model + "\x00" + output))
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

func fakeInputText(messages []runtimeapi.Message) string {
	var parts []string
	for _, message := range messages {
		if text := runtimeapi.MessageText(message); text != "" {
			parts = append(parts, text)
		}
		for _, result := range runtimeapi.MessageToolResults(message) {
			parts = append(parts, result.Content)
		}
	}
	return strings.Join(parts, "\n")
}

func providerToolResultContent(result *runtimeapi.ToolResult) string {
	payload := struct {
		Content   string `json:"content"`
		IsError   bool   `json:"isError"`
		ErrorCode string `json:"errorCode,omitempty"`
		Truncated bool   `json:"truncated,omitempty"`
	}{
		Content:   result.Content,
		IsError:   result.IsError,
		ErrorCode: result.ErrorCode,
		Truncated: result.Truncated,
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
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
