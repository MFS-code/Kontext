package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

func parseFakeToolSequence(value string) ([]FakeToolSequenceEntry, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("KONTEXT_FAKE_TOOL_SEQUENCE is required for tool_sequence scenario")
	}
	var sequence []FakeToolSequenceEntry
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sequence); err != nil {
		return nil, fmt.Errorf("KONTEXT_FAKE_TOOL_SEQUENCE must contain a JSON array of tool calls: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("KONTEXT_FAKE_TOOL_SEQUENCE must contain exactly one JSON value")
	}
	if len(sequence) == 0 {
		return nil, errors.New("KONTEXT_FAKE_TOOL_SEQUENCE must contain at least one tool call")
	}
	for index := range sequence {
		entry := &sequence[index]
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" {
			return nil, fmt.Errorf("KONTEXT_FAKE_TOOL_SEQUENCE entry %d requires name", index)
		}
		if len(entry.Arguments) == 0 {
			return nil, fmt.Errorf("KONTEXT_FAKE_TOOL_SEQUENCE entry %d requires arguments", index)
		}
		var arguments map[string]json.RawMessage
		if err := json.Unmarshal(entry.Arguments, &arguments); err != nil || arguments == nil {
			return nil, fmt.Errorf(
				"KONTEXT_FAKE_TOOL_SEQUENCE entry %d arguments must be a JSON object",
				index,
			)
		}
	}
	return sequence, nil
}

func fakeToolSequenceCompletion(
	request runtimeapi.CompletionRequest,
	sequence []FakeToolSequenceEntry,
) (runtimeapi.CompletionResponse, error) {
	var results []runtimeapi.ToolResult
	var calls []runtimeapi.ToolCall
	pendingCalls := make(map[string]runtimeapi.ToolCall)
	for _, message := range request.Messages {
		for _, call := range runtimeapi.MessageToolCalls(message) {
			pendingCalls[call.ID] = call
		}
		for _, result := range runtimeapi.MessageToolResults(message) {
			call, exists := pendingCalls[result.CallID]
			if !exists {
				return runtimeapi.CompletionResponse{}, fakeSequenceError(
					"fake_tool_sequence_mismatch",
					"fake tool sequence result has no preceding corresponding assistant call",
				)
			}
			delete(pendingCalls, result.CallID)
			calls = append(calls, call)
			results = append(results, result)
		}
	}
	if len(results) > len(sequence) {
		return runtimeapi.CompletionResponse{}, fakeSequenceError(
			"fake_tool_sequence_mismatch",
			"fake tool sequence received more results than configured calls",
		)
	}
	if len(pendingCalls) != 0 || len(calls) != len(results) {
		return runtimeapi.CompletionResponse{}, fakeSequenceError(
			"fake_tool_sequence_mismatch",
			"fake tool sequence results do not have corresponding assistant calls",
		)
	}
	for index := range results {
		expectedCallID := fmt.Sprintf("fake-tool-call-%03d", index+1)
		if results[index].CallID != expectedCallID ||
			calls[index].ID != expectedCallID ||
			results[index].CallID != calls[index].ID {
			return runtimeapi.CompletionResponse{}, fakeSequenceError(
				"fake_tool_sequence_mismatch",
				fmt.Sprintf(
					"fake tool sequence result %d did not match deterministic call %q",
					index,
					expectedCallID,
				),
			)
		}
		if results[index].Name != sequence[index].Name ||
			calls[index].Name != sequence[index].Name ||
			!bytes.Equal(calls[index].Arguments, sequence[index].Arguments) {
			return runtimeapi.CompletionResponse{}, fakeSequenceError(
				"fake_tool_sequence_mismatch",
				fmt.Sprintf(
					"fake tool sequence call %d did not match configured tool %q",
					index,
					sequence[index].Name,
				),
			)
		}
	}
	if len(results) == len(sequence) {
		type resultSummary struct {
			Name      string `json:"name"`
			Content   string `json:"content"`
			IsError   bool   `json:"isError"`
			ErrorCode string `json:"errorCode,omitempty"`
			Truncated bool   `json:"truncated,omitempty"`
		}
		summaries := make([]resultSummary, 0, len(results))
		for _, result := range results {
			summaries = append(summaries, resultSummary{
				Name:      result.Name,
				Content:   result.Content,
				IsError:   result.IsError,
				ErrorCode: result.ErrorCode,
				Truncated: result.Truncated,
			})
		}
		encoded, _ := json.Marshal(summaries)
		return fakeTextResponse(
			request.Model,
			"Fake provider completed tool sequence: "+string(encoded),
			int64(len(strings.Fields(fakeInputText(request.Messages)))),
		), nil
	}

	next := sequence[len(results)]
	if !toolIsExposed(request.Tools, next.Name) {
		return runtimeapi.CompletionResponse{}, fakeSequenceError(
			"fake_tool_unavailable",
			fmt.Sprintf("fake tool sequence tool %q was not exposed", next.Name),
		)
	}
	index := len(results) + 1
	requestHash := sha256.Sum256([]byte(
		fmt.Sprintf("%s\x00%d\x00%s\x00%s", request.Model, index, next.Name, next.Arguments),
	))
	inputTokens := int64(len(strings.Fields(fakeInputText(request.Messages))))
	outputTokens := int64(len(strings.Fields(next.Name + " " + string(next.Arguments))))
	totalTokens := inputTokens + outputTokens
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{{
				Type: runtimeapi.ContentTypeToolCall,
				ToolCall: &runtimeapi.ToolCall{
					ID:        fmt.Sprintf("fake-tool-call-%03d", index),
					Name:      next.Name,
					Arguments: append(json.RawMessage(nil), next.Arguments...),
				},
			}},
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

func toolIsExposed(definitions []runtimeapi.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func fakeSequenceError(code string, message string) error {
	retryable := false
	return &runtimeapi.ProviderError{
		Code:      code,
		Message:   message,
		Retryable: &retryable,
	}
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
