package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
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
	return completeJSON(
		ctx,
		anthropic.client,
		anthropic.endpoint,
		headers,
		payload,
		"anthropic",
		"request-id",
		true,
		normalizeAnthropicResponse,
	)
}

type anthropicRequestPayload struct {
	Model     string                    `json:"model"`
	MaxTokens int64                     `json:"max_tokens"`
	Messages  []anthropicRequestMessage `json:"messages"`
	Tools     []anthropicToolDefinition `json:"tools,omitempty"`
}

type anthropicRequestMessage struct {
	Role    string                    `json:"role"`
	Content []anthropicRequestContent `json:"content"`
}

type anthropicRequestContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func anthropicRequest(request runtimeapi.CompletionRequest) (anthropicRequestPayload, error) {
	if err := validateToolDefinitions(request.Tools); err != nil {
		return anthropicRequestPayload{}, err
	}
	maxTokens := anthropicDefaultMaxTokens
	if request.MaxTokens != nil {
		maxTokens = *request.MaxTokens
	}
	payload := anthropicRequestPayload{
		Model:     request.Model,
		MaxTokens: maxTokens,
		Messages:  make([]anthropicRequestMessage, 0, len(request.Messages)),
		Tools:     make([]anthropicToolDefinition, 0, len(request.Tools)),
	}
	for _, definition := range request.Tools {
		payload.Tools = append(payload.Tools, anthropicToolDefinition{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: append(json.RawMessage(nil), definition.InputSchema...),
		})
	}
	for messageIndex, message := range request.Messages {
		if err := validateMessage(messageIndex, message); err != nil {
			return anthropicRequestPayload{}, err
		}
		role := string(message.Role)
		if message.Role == runtimeapi.RoleTool {
			role = string(runtimeapi.RoleUser)
		}
		normalized := anthropicRequestMessage{
			Role:    role,
			Content: make([]anthropicRequestContent, 0, len(message.Content)),
		}
		for blockIndex, block := range message.Content {
			switch block.Type {
			case runtimeapi.ContentTypeText:
				if message.Role == runtimeapi.RoleTool {
					return anthropicRequestPayload{}, fmt.Errorf(
						"tool message %d content block %d must be a tool result",
						messageIndex,
						blockIndex,
					)
				}
				normalized.Content = append(normalized.Content, anthropicRequestContent{
					Type: "text",
					Text: block.Text,
				})
			case runtimeapi.ContentTypeToolCall:
				if message.Role != runtimeapi.RoleAssistant || block.ToolCall == nil {
					return anthropicRequestPayload{}, fmt.Errorf(
						"message %d content block %d has invalid tool call",
						messageIndex,
						blockIndex,
					)
				}
				normalized.Content = append(normalized.Content, anthropicRequestContent{
					Type:  "tool_use",
					ID:    block.ToolCall.ID,
					Name:  block.ToolCall.Name,
					Input: append(json.RawMessage(nil), block.ToolCall.Arguments...),
				})
			case runtimeapi.ContentTypeToolResult:
				if message.Role != runtimeapi.RoleTool || block.ToolResult == nil {
					return anthropicRequestPayload{}, fmt.Errorf(
						"message %d content block %d has invalid tool result",
						messageIndex,
						blockIndex,
					)
				}
				normalized.Content = append(normalized.Content, anthropicRequestContent{
					Type:      "tool_result",
					ToolUseID: block.ToolResult.CallID,
					Content:   providerToolResultContent(block.ToolResult),
					IsError:   block.ToolResult.IsError,
				})
			default:
				return anthropicRequestPayload{}, fmt.Errorf(
					"message %d content block %d has unsupported type %q",
					messageIndex,
					blockIndex,
					block.Type,
				)
			}
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
