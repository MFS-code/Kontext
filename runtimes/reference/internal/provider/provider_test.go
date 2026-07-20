package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

func TestResolveSelectsFakeProvider(t *testing.T) {
	selected, err := provider.Resolve(config.Config{Provider: "fake"})
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if selected.Name() != "fake" {
		t.Fatalf("unexpected provider %q", selected.Name())
	}
	if _, err := provider.Resolve(config.Config{Provider: "unknown"}); err == nil {
		t.Fatalf("expected unsupported provider error")
	} else {
		var unsupported *provider.UnsupportedError
		if !errors.As(err, &unsupported) {
			t.Fatalf("expected typed unsupported error, got %T", err)
		}
	}
}

func TestResolveSelectsMaintainedHTTPProviders(t *testing.T) {
	tests := []struct {
		name   string
		config config.Config
	}{
		{
			name: "anthropic",
			config: config.Config{
				Provider:        "anthropic",
				AnthropicAPIKey: "anthropic-key",
			},
		},
		{
			name: "openai",
			config: config.Config{
				Provider:     "openai",
				OpenAIAPIKey: "openai-key",
			},
		},
		{
			name: "openai-compatible",
			config: config.Config{
				Provider:     "openai-compatible",
				OpenAIAPIKey: "openai-key",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected, err := provider.Resolve(test.config)
			if err != nil {
				t.Fatalf("resolve provider: %v", err)
			}
			if selected.Name() != test.config.Provider {
				t.Fatalf("expected provider %q, got %q", test.config.Provider, selected.Name())
			}
		})
	}
}

func TestResolveReportsMissingProviderCredentials(t *testing.T) {
	for _, name := range []string{"anthropic", "openai", "openai-compatible"} {
		t.Run(name, func(t *testing.T) {
			_, err := provider.Resolve(config.Config{Provider: name})
			var configurationError *provider.ConfigurationError
			if !errors.As(err, &configurationError) {
				t.Fatalf("expected configuration error, got %v", err)
			}
			if configurationError.Code != "missing_provider_credentials" {
				t.Fatalf("unexpected error code %q", configurationError.Code)
			}
		})
	}
}

func TestResolveValidatesFakeScenarios(t *testing.T) {
	if _, err := provider.Resolve(config.Config{
		Provider:     "fake",
		FakeScenario: "other",
	}); err == nil {
		t.Fatalf("expected unsupported scenario error")
	} else {
		var configurationError *provider.ConfigurationError
		if !errors.As(err, &configurationError) {
			t.Fatalf("expected typed configuration error, got %T", err)
		}
	}
	if _, err := provider.Resolve(config.Config{
		Provider:     "fake",
		FakeScenario: provider.FakeScenarioDelay,
	}); err == nil {
		t.Fatalf("expected missing delay error")
	}
	selected, err := provider.Resolve(config.Config{
		Provider:     "fake",
		FakeScenario: provider.FakeScenarioTool,
		FakeToolName: "zero_argument_tool",
	})
	if err != nil {
		t.Fatalf("resolve zero-argument tool scenario: %v", err)
	}
	fake, ok := selected.(*provider.Fake)
	if !ok || string(fake.ToolArguments) != "{}" {
		t.Fatalf("unexpected zero-argument fake %#v", selected)
	}
	for _, sequence := range []string{
		"",
		`not-json`,
		`[]`,
		`[{"arguments":{}}]`,
		`[{"name":"tool"}]`,
		`[{"name":"tool","arguments":[]}]`,
		`[{"name":"tool","arguments":{},"extra":true}]`,
	} {
		if _, err := provider.Resolve(config.Config{
			Provider:         "fake",
			FakeScenario:     provider.FakeScenarioToolSequence,
			FakeToolSequence: sequence,
		}); err == nil {
			t.Fatalf("expected invalid tool sequence %q to fail", sequence)
		}
	}
}

func TestFakeProviderScenarios(t *testing.T) {
	request := completionRequest("vendor/model@2026:beta")

	t.Run("success", func(t *testing.T) {
		fake := &provider.Fake{Scenario: provider.FakeScenarioSuccess}
		response, err := fake.Complete(context.Background(), request)
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if err := runtimeapi.ValidateResponse(response); err != nil {
			t.Fatalf("validate response: %v", err)
		}
		if got := runtimeapi.MessageText(response.Message); got != "Fake provider completed goal: test goal" {
			t.Fatalf("unexpected output %q", got)
		}
		if response.Usage.InputTokens == nil || *response.Usage.InputTokens != 2 {
			t.Fatalf("unexpected input usage %#v", response.Usage)
		}
		if response.RequestID == "" {
			t.Fatalf("expected deterministic request id")
		}
	})

	t.Run("failure", func(t *testing.T) {
		fake := &provider.Fake{Scenario: provider.FakeScenarioFailure}
		_, err := fake.Complete(context.Background(), request)
		var providerError *runtimeapi.ProviderError
		if !errors.As(err, &providerError) || providerError.Code != "fake_provider_failure" {
			t.Fatalf("unexpected error %v", err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		fake := &provider.Fake{Scenario: provider.FakeScenarioMalformed}
		response, err := fake.Complete(context.Background(), request)
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if err := runtimeapi.ValidateResponse(response); err == nil {
			t.Fatalf("expected malformed response")
		}
	})

	t.Run("delay cancellation", func(t *testing.T) {
		fake := &provider.Fake{
			Scenario: provider.FakeScenarioDelay,
			Delay:    time.Second,
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		started := time.Now()
		_, err := fake.Complete(ctx, request)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
		if time.Since(started) > 100*time.Millisecond {
			t.Fatalf("fake provider did not cancel promptly")
		}
	})

	t.Run("tool round trip", func(t *testing.T) {
		fake := &provider.Fake{
			Scenario:      provider.FakeScenarioTool,
			ToolName:      "read_knowledge",
			ToolArguments: json.RawMessage(`{"path":"guide.txt"}`),
		}
		request := completionRequest("model")
		request.Tools = []runtimeapi.ToolDefinition{
			{
				Name:        "read_knowledge",
				Description: "Read knowledge.",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		}
		response, err := fake.Complete(context.Background(), request)
		if err != nil {
			t.Fatalf("request tool: %v", err)
		}
		calls := runtimeapi.MessageToolCalls(response.Message)
		if len(calls) != 1 || calls[0].Name != "read_knowledge" {
			t.Fatalf("unexpected calls %#v", calls)
		}
		request.Messages = append(request.Messages, response.Message, runtimeapi.Message{
			Role: runtimeapi.RoleTool,
			Content: []runtimeapi.ContentBlock{
				{
					Type: runtimeapi.ContentTypeToolResult,
					ToolResult: &runtimeapi.ToolResult{
						CallID:  calls[0].ID,
						Name:    calls[0].Name,
						Content: "tool loop works",
					},
				},
			},
		})
		response, err = fake.Complete(context.Background(), request)
		if err != nil {
			t.Fatalf("complete after tool: %v", err)
		}
		if got := runtimeapi.MessageText(response.Message); got !=
			"Fake provider received read_knowledge result: tool loop works" {
			t.Fatalf("unexpected final output %q", got)
		}
	})
}

func TestFakeToolSequenceOrdersCallsAndReturnsBoundedResults(t *testing.T) {
	selected, err := provider.Resolve(config.Config{
		Provider:     "fake",
		FakeScenario: provider.FakeScenarioToolSequence,
		FakeToolSequence: `[
			{"name":"first","arguments":{"step":1}},
			{"name":"second","arguments":{"step":2}}
		]`,
	})
	if err != nil {
		t.Fatalf("resolve sequence: %v", err)
	}
	request := completionRequest("model")
	request.Tools = []runtimeapi.ToolDefinition{
		{Name: "first", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "second", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}

	first, err := selected.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("first completion: %v", err)
	}
	firstCall := runtimeapi.MessageToolCalls(first.Message)
	if len(firstCall) != 1 ||
		firstCall[0].ID != "fake-tool-call-001" ||
		firstCall[0].Name != "first" {
		t.Fatalf("unexpected first call %#v", firstCall)
	}
	request.Messages = appendToolResult(
		request.Messages,
		first.Message,
		firstCall[0],
		runtimeapi.ToolResult{Content: "first bounded result"},
	)

	second, err := selected.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("second completion: %v", err)
	}
	secondCall := runtimeapi.MessageToolCalls(second.Message)
	if len(secondCall) != 1 ||
		secondCall[0].ID != "fake-tool-call-002" ||
		secondCall[0].Name != "second" {
		t.Fatalf("unexpected second call %#v", secondCall)
	}
	request.Messages = appendToolResult(
		request.Messages,
		second.Message,
		secondCall[0],
		runtimeapi.ToolResult{
			Content:   "second bounded result",
			IsError:   true,
			ErrorCode: "expected_error",
			Truncated: true,
		},
	)

	final, err := selected.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("terminal completion: %v", err)
	}
	output := runtimeapi.MessageText(final.Message)
	for _, expected := range []string{
		"Fake provider completed tool sequence:",
		"first bounded result",
		"second bounded result",
		`"errorCode":"expected_error"`,
		`"truncated":true`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("terminal output %q does not contain %q", output, expected)
		}
	}
}

func TestFakeToolSequenceRejectsMissingAndOutOfOrderTools(t *testing.T) {
	selected, err := provider.Resolve(config.Config{
		Provider:         "fake",
		FakeScenario:     provider.FakeScenarioToolSequence,
		FakeToolSequence: `[{"name":"required","arguments":{}}]`,
	})
	if err != nil {
		t.Fatalf("resolve sequence: %v", err)
	}
	request := completionRequest("model")
	if _, err := selected.Complete(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), `"required" was not exposed`) {
		t.Fatalf("expected missing tool error, got %v", err)
	}

	request.Tools = []runtimeapi.ToolDefinition{{Name: "required"}}
	request.Messages = append(request.Messages, runtimeapi.Message{
		Role: runtimeapi.RoleAssistant,
		Content: []runtimeapi.ContentBlock{{
			Type: runtimeapi.ContentTypeToolCall,
			ToolCall: &runtimeapi.ToolCall{
				ID:        "fake-tool-call-001",
				Name:      "different",
				Arguments: json.RawMessage(`{}`),
			},
		}},
	}, runtimeapi.Message{
		Role: runtimeapi.RoleTool,
		Content: []runtimeapi.ContentBlock{{
			Type: runtimeapi.ContentTypeToolResult,
			ToolResult: &runtimeapi.ToolResult{
				CallID: "fake-tool-call-001",
				Name:   "different",
			},
		}},
	})
	if _, err := selected.Complete(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), `configured tool "required"`) {
		t.Fatalf("expected ordering error, got %v", err)
	}
}

func TestFakeToolSequenceRejectsUncorrelatedCallIDs(t *testing.T) {
	selected, err := provider.Resolve(config.Config{
		Provider:         "fake",
		FakeScenario:     provider.FakeScenarioToolSequence,
		FakeToolSequence: `[{"name":"required","arguments":{}}]`,
	})
	if err != nil {
		t.Fatalf("resolve sequence: %v", err)
	}
	for _, test := range []struct {
		name      string
		assistant bool
		callID    string
		resultID  string
	}{
		{name: "missing assistant call", assistant: false, resultID: "fake-tool-call-001"},
		{name: "unexpected deterministic id", assistant: true, callID: "other", resultID: "other"},
		{name: "mismatched result id", assistant: true, callID: "fake-tool-call-001", resultID: "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := completionRequest("model")
			request.Tools = []runtimeapi.ToolDefinition{{Name: "required"}}
			if test.assistant {
				request.Messages = append(request.Messages, runtimeapi.Message{
					Role: runtimeapi.RoleAssistant,
					Content: []runtimeapi.ContentBlock{{
						Type: runtimeapi.ContentTypeToolCall,
						ToolCall: &runtimeapi.ToolCall{
							ID:        test.callID,
							Name:      "required",
							Arguments: json.RawMessage(`{}`),
						},
					}},
				})
			}
			request.Messages = append(request.Messages, runtimeapi.Message{
				Role: runtimeapi.RoleTool,
				Content: []runtimeapi.ContentBlock{{
					Type: runtimeapi.ContentTypeToolResult,
					ToolResult: &runtimeapi.ToolResult{
						CallID: test.resultID,
						Name:   "required",
					},
				}},
			})
			if _, err := selected.Complete(context.Background(), request); err == nil ||
				!strings.Contains(err.Error(), "fake_tool_sequence_mismatch") {
				t.Fatalf("expected correlation error, got %v", err)
			}
		})
	}
}

func appendToolResult(
	messages []runtimeapi.Message,
	assistant runtimeapi.Message,
	call runtimeapi.ToolCall,
	result runtimeapi.ToolResult,
) []runtimeapi.Message {
	result.CallID = call.ID
	result.Name = call.Name
	return append(messages, assistant, runtimeapi.Message{
		Role: runtimeapi.RoleTool,
		Content: []runtimeapi.ContentBlock{{
			Type:       runtimeapi.ContentTypeToolResult,
			ToolResult: &result,
		}},
	})
}

func completionRequest(model string) runtimeapi.CompletionRequest {
	return runtimeapi.CompletionRequest{
		Model: model,
		Messages: []runtimeapi.Message{
			{
				Role: runtimeapi.RoleUser,
				Content: []runtimeapi.ContentBlock{
					{Type: runtimeapi.ContentTypeText, Text: "test goal"},
				},
			},
		},
	}
}
