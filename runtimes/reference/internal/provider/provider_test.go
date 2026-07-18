package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
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
