package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

const (
	FakeScenarioSuccess      = "success"
	FakeScenarioFailure      = "failure"
	FakeScenarioMalformed    = "malformed"
	FakeScenarioDelay        = "delay"
	FakeScenarioTool         = "tool"
	FakeScenarioToolSequence = "tool_sequence"
)

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
	case FakeScenarioToolSequence:
		sequence, err := parseFakeToolSequence(runtimeConfig.FakeToolSequence)
		if err != nil {
			return nil, &ConfigurationError{
				Provider: "fake",
				Code:     "invalid_fake_tool_sequence",
				Message:  err.Error(),
			}
		}
		return &Fake{
			Scenario:     scenario,
			ToolSequence: sequence,
		}, nil
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

type FakeToolSequenceEntry struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Fake struct {
	Scenario      string
	Delay         time.Duration
	ToolName      string
	ToolArguments json.RawMessage
	ToolSequence  []FakeToolSequenceEntry
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
	case FakeScenarioToolSequence:
		return fakeToolSequenceCompletion(request, fake.ToolSequence)
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
	if !toolIsExposed(request.Tools, toolName) {
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
