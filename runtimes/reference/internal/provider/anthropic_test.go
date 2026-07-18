package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

func TestAnthropicCompletesAndNormalizesResponse(t *testing.T) {
	var received struct {
		Model     string `json:"model"`
		MaxTokens int64  `json:"max_tokens"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/proxy/v1/messages" {
			t.Errorf("unexpected request path %q", request.URL.Path)
		}
		if request.Header.Get("x-api-key") != "anthropic-test-key" {
			t.Errorf("missing Anthropic API key header")
		}
		if request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("missing Anthropic version header")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("request-id", "req-anthropic-1")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"role":"assistant",
			"content":[
				{"type":"text","text":"calling a tool"},
				{"type":"tool_use","id":"tool-1","name":"lookup","input":{"query":"status"}}
			],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":0,"output_tokens":12}
		}`))
	}))
	defer server.Close()

	selected, err := provider.NewAnthropic(provider.AnthropicConfig{
		APIKey:  "anthropic-test-key",
		BaseURL: server.URL + "/proxy",
		Client:  server.Client(),
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	response, err := selected.Complete(
		context.Background(),
		completionRequest("claude/model@opaque"),
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if received.Model != "claude/model@opaque" {
		t.Fatalf("model identifier changed: %q", received.Model)
	}
	if received.MaxTokens != 4096 {
		t.Fatalf("unexpected default max tokens %d", received.MaxTokens)
	}
	if response.RequestID != "req-anthropic-1" {
		t.Fatalf("unexpected request id %q", response.RequestID)
	}
	if response.StopReason != runtimeapi.StopReasonToolUse {
		t.Fatalf("unexpected stop reason %q", response.StopReason)
	}
	if response.Usage.InputTokens == nil || *response.Usage.InputTokens != 0 {
		t.Fatalf("measured zero input tokens were lost: %#v", response.Usage)
	}
	if response.Usage.OutputTokens == nil || *response.Usage.OutputTokens != 12 {
		t.Fatalf("unexpected output usage: %#v", response.Usage)
	}
	if response.Usage.TotalTokens != nil {
		t.Fatalf("unreported total tokens must remain absent: %#v", response.Usage)
	}
	if len(response.Message.Content) != 2 ||
		response.Message.Content[1].ToolCall == nil ||
		response.Message.Content[1].ToolCall.Name != "lookup" ||
		string(response.Message.Content[1].ToolCall.Arguments) != `{"query":"status"}` {
		t.Fatalf("unexpected normalized content %#v", response.Message.Content)
	}
	if err := runtimeapi.ValidateResponse(response); err != nil {
		t.Fatalf("validate normalized response: %v", err)
	}
}

func TestAnthropicUsesExactEndpointAndRequestedTokenLimit(t *testing.T) {
	var maxTokens int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/custom/messages" {
			t.Errorf("unexpected exact endpoint path %q", request.URL.Path)
		}
		var payload struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		_ = json.NewDecoder(request.Body).Decode(&payload)
		maxTokens = payload.MaxTokens
		_, _ = writer.Write([]byte(`{
			"role":"assistant",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn"
		}`))
	}))
	defer server.Close()

	selected, err := provider.NewAnthropic(provider.AnthropicConfig{
		APIKey:   "key",
		Endpoint: server.URL + "/custom/messages",
		Client:   server.Client(),
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	request := completionRequest("model")
	limit := int64(77)
	request.MaxTokens = &limit
	response, err := selected.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if maxTokens != limit {
		t.Fatalf("expected max tokens %d, got %d", limit, maxTokens)
	}
	if response.Usage.InputTokens != nil ||
		response.Usage.OutputTokens != nil ||
		response.Usage.TotalTokens != nil {
		t.Fatalf("absent usage must remain absent: %#v", response.Usage)
	}
}

func TestAnthropicNormalizesHTTPFailures(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		errorBody string
		code      string
		retryable bool
	}{
		{
			name:      "authentication",
			status:    http.StatusUnauthorized,
			errorBody: `{"error":{"message":"invalid key"},"request_id":"body-request"}`,
			code:      "authentication_failed",
		},
		{
			name:      "rate limit",
			status:    http.StatusTooManyRequests,
			errorBody: `{"error":{"message":"slow down"}}`,
			code:      "rate_limited",
			retryable: true,
		},
		{
			name:      "endpoint failure",
			status:    http.StatusServiceUnavailable,
			errorBody: `{"error":{"message":"temporarily unavailable"}}`,
			code:      "provider_endpoint_error",
			retryable: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("request-id", "header-request")
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.errorBody))
			}))
			defer server.Close()
			selected, err := provider.NewAnthropic(provider.AnthropicConfig{
				APIKey:   "key",
				Endpoint: server.URL,
				Client:   server.Client(),
			})
			if err != nil {
				t.Fatalf("create provider: %v", err)
			}
			_, err = selected.Complete(context.Background(), completionRequest("model"))
			var providerError *runtimeapi.ProviderError
			if !errors.As(err, &providerError) {
				t.Fatalf("expected provider error, got %v", err)
			}
			if providerError.Code != test.code {
				t.Fatalf("expected code %q, got %q", test.code, providerError.Code)
			}
			if providerError.Retryable == nil || *providerError.Retryable != test.retryable {
				t.Fatalf("unexpected retryability %#v", providerError.Retryable)
			}
			if providerError.RequestID != "header-request" {
				t.Fatalf("unexpected request id %q", providerError.RequestID)
			}
		})
	}
}

func TestAnthropicRejectsInvalidResponsesAndTimesOut(t *testing.T) {
	t.Run("malformed JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"role":`))
		}))
		defer server.Close()
		selected, err := provider.NewAnthropic(provider.AnthropicConfig{
			APIKey:   "key",
			Endpoint: server.URL,
			Client:   server.Client(),
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		_, err = selected.Complete(context.Background(), completionRequest("model"))
		assertProviderErrorCode(t, err, "invalid_provider_response")
	})

	t.Run("partial response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"role":"assistant","content":[],"stop_reason":"end_turn"}`))
		}))
		defer server.Close()
		selected, err := provider.NewAnthropic(provider.AnthropicConfig{
			APIKey:   "key",
			Endpoint: server.URL,
			Client:   server.Client(),
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		response, err := selected.Complete(context.Background(), completionRequest("model"))
		if err != nil {
			t.Fatalf("transport normalization: %v", err)
		}
		if err := runtimeapi.ValidateResponse(response); err == nil {
			t.Fatal("expected response validation failure")
		}
	})

	t.Run("HTTP client timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			_, _ = writer.Write([]byte(`{}`))
		}))
		defer server.Close()
		selected, err := provider.NewAnthropic(provider.AnthropicConfig{
			APIKey:   "key",
			Endpoint: server.URL,
			Client:   &http.Client{Timeout: 10 * time.Millisecond},
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		_, err = selected.Complete(context.Background(), completionRequest("model"))
		assertProviderErrorCode(t, err, "provider_timeout")
	})
}

func TestAnthropicSerializesToolsCallsAndResults(t *testing.T) {
	var received struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				Content   string          `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = writer.Write([]byte(`{
			"role":"assistant",
			"content":[{"type":"text","text":"done"}],
			"stop_reason":"end_turn"
		}`))
	}))
	defer server.Close()
	selected, err := provider.NewAnthropic(provider.AnthropicConfig{
		APIKey:   "key",
		Endpoint: server.URL,
		Client:   server.Client(),
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	request := completionRequest("model")
	request.Tools = []runtimeapi.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Look up a status.",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	}
	request.Messages = append(request.Messages,
		runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{
					Type: runtimeapi.ContentTypeToolCall,
					ToolCall: &runtimeapi.ToolCall{
						ID:        "call-1",
						Name:      "lookup",
						Arguments: json.RawMessage(`{"query":"status"}`),
					},
				},
			},
		},
		runtimeapi.Message{
			Role: runtimeapi.RoleTool,
			Content: []runtimeapi.ContentBlock{
				{
					Type: runtimeapi.ContentTypeToolResult,
					ToolResult: &runtimeapi.ToolResult{
						CallID:  "call-1",
						Name:    "lookup",
						Content: `{"status":"ok"}`,
					},
				},
			},
		},
	)
	if _, err := selected.Complete(context.Background(), request); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(received.Tools) != 1 ||
		received.Tools[0].Name != "lookup" ||
		!json.Valid(received.Tools[0].InputSchema) {
		t.Fatalf("unexpected tools %#v", received.Tools)
	}
	if len(received.Messages) != 3 ||
		received.Messages[1].Content[0].Type != "tool_use" ||
		received.Messages[2].Role != "user" ||
		received.Messages[2].Content[0].Type != "tool_result" ||
		received.Messages[2].Content[0].ToolUseID != "call-1" {
		t.Fatalf("unexpected messages %#v", received.Messages)
	}
}

func TestAnthropicRequiresWellFormedCredentials(t *testing.T) {
	for _, apiKey := range []string{"", " key-with-spaces ", "key\ninjected"} {
		_, err := provider.NewAnthropic(provider.AnthropicConfig{APIKey: apiKey})
		var configurationError *provider.ConfigurationError
		if !errors.As(err, &configurationError) {
			t.Fatalf("expected credential configuration error for %q, got %v", apiKey, err)
		}
	}
}

func assertProviderErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var providerError *runtimeapi.ProviderError
	if !errors.As(err, &providerError) {
		t.Fatalf("expected provider error %q, got %v", code, err)
	}
	if providerError.Code != code {
		t.Fatalf("expected provider error %q, got %q", code, providerError.Code)
	}
}
