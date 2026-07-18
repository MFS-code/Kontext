package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

func TestOpenAICompletesAgainstCompatibleBaseURL(t *testing.T) {
	var received struct {
		Model               string `json:"model"`
		MaxCompletionTokens *int64 `json:"max_completion_tokens"`
		Messages            []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/openai/v1/chat/completions" {
			t.Errorf("unexpected request path %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer openai-test-key" {
			t.Errorf("missing OpenAI bearer token")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("x-request-id", "req-openai-1")
		_, _ = writer.Write([]byte(`{
			"choices":[{
				"message":{
					"role":"assistant",
					"content":"working",
					"tool_calls":[{
						"id":"call-1",
						"type":"function",
						"function":{"name":"lookup","arguments":"{\"query\":\"status\"}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":9,"completion_tokens":0,"total_tokens":9}
		}`))
	}))
	defer server.Close()

	selected, err := provider.NewOpenAI(provider.OpenAIConfig{
		Name:    "openai-compatible",
		APIKey:  "openai-test-key",
		BaseURL: server.URL + "/openai/v1",
		Client:  server.Client(),
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	request := completionRequest("vendor/model:opaque")
	limit := int64(50)
	request.MaxTokens = &limit
	response, err := selected.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if selected.Name() != "openai-compatible" {
		t.Fatalf("unexpected provider name %q", selected.Name())
	}
	if received.Model != "vendor/model:opaque" {
		t.Fatalf("model identifier changed: %q", received.Model)
	}
	if received.MaxCompletionTokens == nil || *received.MaxCompletionTokens != limit {
		t.Fatalf("unexpected token limit %#v", received.MaxCompletionTokens)
	}
	if len(received.Messages) != 1 ||
		received.Messages[0].Role != "user" ||
		received.Messages[0].Content != "test goal" {
		t.Fatalf("unexpected messages %#v", received.Messages)
	}
	if response.RequestID != "req-openai-1" ||
		response.StopReason != runtimeapi.StopReasonToolUse {
		t.Fatalf("unexpected normalized response %#v", response)
	}
	if response.Usage.OutputTokens == nil || *response.Usage.OutputTokens != 0 {
		t.Fatalf("measured zero output tokens were lost: %#v", response.Usage)
	}
	if len(response.Message.Content) != 2 ||
		response.Message.Content[1].ToolCall == nil ||
		response.Message.Content[1].ToolCall.ID != "call-1" {
		t.Fatalf("unexpected content %#v", response.Message.Content)
	}
	if err := runtimeapi.ValidateResponse(response); err != nil {
		t.Fatalf("validate normalized response: %v", err)
	}
}

func TestOpenAILeavesOptionalRequestAndUsageFieldsAbsent(t *testing.T) {
	var hasTokenLimit bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]json.RawMessage
		_ = json.NewDecoder(request.Body).Decode(&payload)
		_, hasTokenLimit = payload["max_completion_tokens"]
		_, _ = writer.Write([]byte(`{
			"choices":[{
				"message":{"role":"assistant","content":"ok"},
				"finish_reason":"stop"
			}]
		}`))
	}))
	defer server.Close()
	selected, err := provider.NewOpenAI(provider.OpenAIConfig{
		APIKey:   "key",
		Endpoint: server.URL,
		Client:   server.Client(),
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	response, err := selected.Complete(context.Background(), completionRequest("model"))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if hasTokenLimit {
		t.Fatal("omitted token budget must not be sent as zero")
	}
	if response.Usage.InputTokens != nil ||
		response.Usage.OutputTokens != nil ||
		response.Usage.TotalTokens != nil {
		t.Fatalf("absent usage must remain absent: %#v", response.Usage)
	}
}

func TestOpenAINormalizesHTTPFailures(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		code      string
		retryable bool
	}{
		{
			name:   "authentication",
			status: http.StatusForbidden,
			code:   "authentication_failed",
		},
		{
			name:      "rate limit",
			status:    http.StatusTooManyRequests,
			code:      "rate_limited",
			retryable: true,
		},
		{
			name:   "bad compatible endpoint",
			status: http.StatusNotFound,
			code:   "provider_request_rejected",
		},
		{
			name:      "server failure",
			status:    http.StatusBadGateway,
			code:      "provider_endpoint_error",
			retryable: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("x-request-id", "openai-error-request")
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{"error":{"message":"provider rejected request"}}`))
			}))
			defer server.Close()
			selected, err := provider.NewOpenAI(provider.OpenAIConfig{
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
			if providerError.RequestID != "openai-error-request" {
				t.Fatalf("unexpected request id %q", providerError.RequestID)
			}
		})
	}
}

func TestOpenAIRejectsInvalidAndPartialResponses(t *testing.T) {
	responses := map[string]string{
		"malformed JSON":  `{"choices":`,
		"missing choices": `{"usage":{"prompt_tokens":1}}`,
		"invalid tool arguments": `{
			"choices":[{
				"message":{
					"role":"assistant",
					"content":null,
					"tool_calls":[{
						"id":"call-1",
						"type":"function",
						"function":{"name":"lookup","arguments":"{"}
					}]
				},
				"finish_reason":"tool_calls"
			}]
		}`,
	}
	for name, body := range responses {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(body))
			}))
			defer server.Close()
			selected, err := provider.NewOpenAI(provider.OpenAIConfig{
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
	}
}

func TestOpenAIRequiresWellFormedCredentials(t *testing.T) {
	for _, apiKey := range []string{"", " key-with-spaces ", "key\r\ninjected"} {
		_, err := provider.NewOpenAI(provider.OpenAIConfig{APIKey: apiKey})
		var configurationError *provider.ConfigurationError
		if !errors.As(err, &configurationError) {
			t.Fatalf("expected credential configuration error for %q, got %v", apiKey, err)
		}
	}
}
