package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

const (
	DefaultAnthropicEndpoint  = "https://api.anthropic.com/v1/messages"
	anthropicMessagesPath     = "v1/messages"
	anthropicVersion          = "2023-06-01"
	anthropicDefaultMaxTokens = int64(4096)
)

type AnthropicConfig struct {
	APIKey   string
	Endpoint string
	BaseURL  string
	Client   HTTPClient
}

type Anthropic struct {
	apiKey   string
	endpoint string
	client   HTTPClient
}

func NewAnthropic(config AnthropicConfig) (*Anthropic, error) {
	if err := validateAPIKey("anthropic", config.APIKey); err != nil {
		return nil, err
	}
	endpoint, err := providerEndpoint(
		"anthropic",
		config.Endpoint,
		config.BaseURL,
		DefaultAnthropicEndpoint,
		anthropicMessagesPath,
	)
	if err != nil {
		return nil, err
	}
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &Anthropic{
		apiKey:   config.APIKey,
		endpoint: endpoint,
		client:   client,
	}, nil
}

func (anthropic *Anthropic) Name() string {
	return "anthropic"
}

func (anthropic *Anthropic) Complete(
	ctx context.Context,
	request runtimeapi.CompletionRequest,
) (runtimeapi.CompletionResponse, error) {
	payload, err := anthropicRequest(request)
	if err != nil {
		return runtimeapi.CompletionResponse{}, err
	}
	headers := make(http.Header)
	headers.Set("x-api-key", anthropic.apiKey)
	headers.Set("anthropic-version", anthropicVersion)
	result, err := sendJSON(ctx, anthropic.client, anthropic.endpoint, headers, payload)
	if err != nil {
		return runtimeapi.CompletionResponse{}, err
	}

	responseRequestID := requestID(result.Header, "request-id")
	if result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices {
		var failure anthropicErrorResponse
		_ = json.Unmarshal(result.Body, &failure)
		if responseRequestID == "" {
			responseRequestID = strings.TrimSpace(failure.RequestID)
		}
		return runtimeapi.CompletionResponse{}, providerHTTPError(
			"anthropic",
			result.StatusCode,
			failure.Error.Message,
			responseRequestID,
		)
	}

	var response anthropicResponse
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return runtimeapi.CompletionResponse{}, invalidResponse(
			fmt.Sprintf("anthropic returned malformed JSON: %v", err),
			result.StatusCode,
			responseRequestID,
		)
	}
	normalized, err := normalizeAnthropicResponse(response, responseRequestID)
	if err != nil {
		return runtimeapi.CompletionResponse{}, invalidResponse(
			fmt.Sprintf("anthropic returned an invalid response: %v", err),
			result.StatusCode,
			responseRequestID,
		)
	}
	return normalized, nil
}

type anthropicRequestPayload struct {
	Model     string                    `json:"model"`
	MaxTokens int64                     `json:"max_tokens"`
	Messages  []anthropicRequestMessage `json:"messages"`
}

type anthropicRequestMessage struct {
	Role    string                    `json:"role"`
	Content []anthropicRequestContent `json:"content"`
}

type anthropicRequestContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func anthropicRequest(request runtimeapi.CompletionRequest) (anthropicRequestPayload, error) {
	maxTokens := anthropicDefaultMaxTokens
	if request.MaxTokens != nil {
		maxTokens = *request.MaxTokens
	}
	payload := anthropicRequestPayload{
		Model:     request.Model,
		MaxTokens: maxTokens,
		Messages:  make([]anthropicRequestMessage, 0, len(request.Messages)),
	}
	for messageIndex, message := range request.Messages {
		if message.Role != runtimeapi.RoleUser && message.Role != runtimeapi.RoleAssistant {
			return anthropicRequestPayload{}, fmt.Errorf(
				"message %d has unsupported role %q",
				messageIndex,
				message.Role,
			)
		}
		normalized := anthropicRequestMessage{
			Role:    string(message.Role),
			Content: make([]anthropicRequestContent, 0, len(message.Content)),
		}
		for blockIndex, block := range message.Content {
			if block.Type != runtimeapi.ContentTypeText {
				return anthropicRequestPayload{}, fmt.Errorf(
					"message %d content block %d has unsupported type %q",
					messageIndex,
					blockIndex,
					block.Type,
				)
			}
			normalized.Content = append(normalized.Content, anthropicRequestContent{
				Type: "text",
				Text: block.Text,
			})
		}
		payload.Messages = append(payload.Messages, normalized)
	}
	return payload, nil
}

type anthropicResponse struct {
	Role       string                     `json:"role"`
	Content    []anthropicResponseContent `json:"content"`
	StopReason string                     `json:"stop_reason"`
	Usage      anthropicUsage             `json:"usage"`
}

type anthropicResponseContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

type anthropicErrorResponse struct {
	RequestID string `json:"request_id"`
	Error     struct {
		Message string `json:"message"`
	} `json:"error"`
}

func normalizeAnthropicResponse(
	response anthropicResponse,
	responseRequestID string,
) (runtimeapi.CompletionResponse, error) {
	if response.Role != string(runtimeapi.RoleAssistant) {
		return runtimeapi.CompletionResponse{}, fmt.Errorf(
			"unexpected message role %q",
			response.Role,
		)
	}
	content := make([]runtimeapi.ContentBlock, 0, len(response.Content))
	for index, block := range response.Content {
		switch block.Type {
		case "text":
			content = append(content, runtimeapi.ContentBlock{
				Type: runtimeapi.ContentTypeText,
				Text: block.Text,
			})
		case "tool_use":
			content = append(content, runtimeapi.ContentBlock{
				Type: runtimeapi.ContentTypeToolCall,
				ToolCall: &runtimeapi.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Arguments: append(json.RawMessage(nil), block.Input...),
				},
			})
		default:
			return runtimeapi.CompletionResponse{}, fmt.Errorf(
				"content block %d has unsupported type %q",
				index,
				block.Type,
			)
		}
	}
	stopReason, err := normalizeAnthropicStopReason(response.StopReason)
	if err != nil {
		return runtimeapi.CompletionResponse{}, err
	}
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role:    runtimeapi.RoleAssistant,
			Content: content,
		},
		Usage: runtimeapi.Usage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
		},
		StopReason: stopReason,
		RequestID:  responseRequestID,
	}, nil
}

func normalizeAnthropicStopReason(value string) (runtimeapi.StopReason, error) {
	switch value {
	case "end_turn":
		return runtimeapi.StopReasonEndTurn, nil
	case "max_tokens":
		return runtimeapi.StopReasonMaxTokens, nil
	case "stop_sequence":
		return runtimeapi.StopReasonStopSequence, nil
	case "tool_use":
		return runtimeapi.StopReasonToolUse, nil
	case "pause_turn":
		return runtimeapi.StopReasonPauseTurn, nil
	case "refusal":
		return runtimeapi.StopReasonRefusal, nil
	case "model_context_window_exceeded":
		return runtimeapi.StopReasonModelContextWindowExceeded, nil
	default:
		return "", fmt.Errorf("unsupported stop reason %q", value)
	}
}
