package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

const (
	DefaultOpenAIEndpoint = "https://api.openai.com/v1/chat/completions"
	openAIChatPath        = "chat/completions"
)

type OpenAIConfig struct {
	Name     string
	APIKey   string
	Endpoint string
	BaseURL  string
	Client   HTTPClient
}

type OpenAI struct {
	name     string
	apiKey   string
	endpoint string
	client   HTTPClient
}

func NewOpenAI(config OpenAIConfig) (*OpenAI, error) {
	if err := validateAPIKey("openai", config.APIKey); err != nil {
		return nil, err
	}
	endpoint, err := providerEndpoint(
		"openai",
		config.Endpoint,
		config.BaseURL,
		DefaultOpenAIEndpoint,
		openAIChatPath,
	)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "openai"
	}
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAI{
		name:     name,
		apiKey:   config.APIKey,
		endpoint: endpoint,
		client:   client,
	}, nil
}

func (openAI *OpenAI) Name() string {
	return openAI.name
}

func (openAI *OpenAI) Complete(
	ctx context.Context,
	request runtimeapi.CompletionRequest,
) (runtimeapi.CompletionResponse, error) {
	payload, err := openAIRequest(request)
	if err != nil {
		return runtimeapi.CompletionResponse{}, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+openAI.apiKey)
	result, err := sendJSON(ctx, openAI.client, openAI.endpoint, headers, payload)
	if err != nil {
		return runtimeapi.CompletionResponse{}, err
	}

	responseRequestID := requestID(result.Header, "x-request-id")
	if result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices {
		var failure openAIErrorResponse
		_ = json.Unmarshal(result.Body, &failure)
		return runtimeapi.CompletionResponse{}, providerHTTPError(
			openAI.name,
			result.StatusCode,
			failure.Error.Message,
			responseRequestID,
		)
	}

	var response openAIResponse
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return runtimeapi.CompletionResponse{}, invalidResponse(
			fmt.Sprintf("%s returned malformed JSON: %v", openAI.name, err),
			result.StatusCode,
			responseRequestID,
		)
	}
	normalized, err := normalizeOpenAIResponse(response, responseRequestID)
	if err != nil {
		return runtimeapi.CompletionResponse{}, invalidResponse(
			fmt.Sprintf("%s returned an invalid response: %v", openAI.name, err),
			result.StatusCode,
			responseRequestID,
		)
	}
	return normalized, nil
}

type openAIRequestPayload struct {
	Model               string                 `json:"model"`
	Messages            []openAIRequestMessage `json:"messages"`
	MaxCompletionTokens *int64                 `json:"max_completion_tokens,omitempty"`
	Tools               []openAIToolDefinition `json:"tools,omitempty"`
}

type openAIRequestMessage struct {
	Role       string                  `json:"role"`
	Content    *string                 `json:"content"`
	ToolCalls  []openAIRequestToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
}

type openAIRequestToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIToolDefinition struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func openAIRequest(request runtimeapi.CompletionRequest) (openAIRequestPayload, error) {
	payload := openAIRequestPayload{
		Model:               request.Model,
		Messages:            make([]openAIRequestMessage, 0, len(request.Messages)),
		MaxCompletionTokens: request.MaxTokens,
		Tools:               make([]openAIToolDefinition, 0, len(request.Tools)),
	}
	for index, definition := range request.Tools {
		if strings.TrimSpace(definition.Name) == "" ||
			strings.TrimSpace(definition.Description) == "" ||
			!json.Valid(definition.InputSchema) {
			return openAIRequestPayload{}, fmt.Errorf("tool definition %d is invalid", index)
		}
		normalized := openAIToolDefinition{Type: "function"}
		normalized.Function.Name = definition.Name
		normalized.Function.Description = definition.Description
		normalized.Function.Parameters = append(
			json.RawMessage(nil),
			definition.InputSchema...,
		)
		payload.Tools = append(payload.Tools, normalized)
	}
	for index, message := range request.Messages {
		if message.Role != runtimeapi.RoleUser &&
			message.Role != runtimeapi.RoleAssistant &&
			message.Role != runtimeapi.RoleTool {
			return openAIRequestPayload{}, fmt.Errorf(
				"message %d has unsupported role %q",
				index,
				message.Role,
			)
		}
		if message.Role == runtimeapi.RoleTool {
			for blockIndex, block := range message.Content {
				if block.Type != runtimeapi.ContentTypeToolResult || block.ToolResult == nil {
					return openAIRequestPayload{}, fmt.Errorf(
						"tool message %d content block %d must be a tool result",
						index,
						blockIndex,
					)
				}
				content := providerToolResultContent(block.ToolResult)
				payload.Messages = append(payload.Messages, openAIRequestMessage{
					Role:       string(runtimeapi.RoleTool),
					Content:    &content,
					ToolCallID: block.ToolResult.CallID,
				})
			}
			continue
		}
		normalized := openAIRequestMessage{Role: string(message.Role)}
		text := runtimeapi.MessageText(message)
		if text != "" {
			normalized.Content = &text
		}
		for blockIndex, block := range message.Content {
			switch block.Type {
			case runtimeapi.ContentTypeText:
			case runtimeapi.ContentTypeToolCall:
				if message.Role != runtimeapi.RoleAssistant || block.ToolCall == nil {
					return openAIRequestPayload{}, fmt.Errorf(
						"message %d content block %d has invalid tool call",
						index,
						blockIndex,
					)
				}
				toolCall := openAIRequestToolCall{
					ID:   block.ToolCall.ID,
					Type: "function",
				}
				toolCall.Function.Name = block.ToolCall.Name
				toolCall.Function.Arguments = string(block.ToolCall.Arguments)
				normalized.ToolCalls = append(normalized.ToolCalls, toolCall)
			default:
				return openAIRequestPayload{}, fmt.Errorf(
					"message %d content block %d has unsupported type %q",
					index,
					blockIndex,
					block.Type,
				)
			}
		}
		payload.Messages = append(payload.Messages, normalized)
	}
	return payload, nil
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

type openAIChoice struct {
	Message      openAIResponseMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type openAIResponseMessage struct {
	Role      string           `json:"role"`
	Content   *string          `json:"content"`
	Refusal   *string          `json:"refusal"`
	ToolCalls []openAIToolCall `json:"tool_calls"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIUsage struct {
	PromptTokens           *int64                       `json:"prompt_tokens"`
	CompletionTokens       *int64                       `json:"completion_tokens"`
	TotalTokens            *int64                       `json:"total_tokens"`
	CompletionTokenDetails openAICompletionTokenDetails `json:"completion_tokens_details"`
}

type openAICompletionTokenDetails struct {
	ReasoningTokens *int64 `json:"reasoning_tokens"`
}

type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func normalizeOpenAIResponse(
	response openAIResponse,
	responseRequestID string,
) (runtimeapi.CompletionResponse, error) {
	if len(response.Choices) == 0 {
		return runtimeapi.CompletionResponse{}, fmt.Errorf("response has no choices")
	}
	choice := response.Choices[0]
	if choice.Message.Role != string(runtimeapi.RoleAssistant) {
		return runtimeapi.CompletionResponse{}, fmt.Errorf(
			"unexpected message role %q",
			choice.Message.Role,
		)
	}

	content := make([]runtimeapi.ContentBlock, 0, 1+len(choice.Message.ToolCalls))
	if choice.Message.Content != nil && strings.TrimSpace(*choice.Message.Content) != "" {
		content = append(content, runtimeapi.ContentBlock{
			Type: runtimeapi.ContentTypeText,
			Text: *choice.Message.Content,
		})
	} else if choice.Message.Refusal != nil && strings.TrimSpace(*choice.Message.Refusal) != "" {
		content = append(content, runtimeapi.ContentBlock{
			Type: runtimeapi.ContentTypeText,
			Text: *choice.Message.Refusal,
		})
	}
	for index, toolCall := range choice.Message.ToolCalls {
		if toolCall.Type != "function" {
			return runtimeapi.CompletionResponse{}, fmt.Errorf(
				"tool call %d has unsupported type %q",
				index,
				toolCall.Type,
			)
		}
		arguments := json.RawMessage(toolCall.Function.Arguments)
		if !json.Valid(arguments) {
			return runtimeapi.CompletionResponse{}, fmt.Errorf(
				"tool call %d has malformed JSON arguments",
				index,
			)
		}
		content = append(content, runtimeapi.ContentBlock{
			Type: runtimeapi.ContentTypeToolCall,
			ToolCall: &runtimeapi.ToolCall{
				ID:        toolCall.ID,
				Name:      toolCall.Function.Name,
				Arguments: append(json.RawMessage(nil), arguments...),
			},
		})
	}
	stopReason, err := normalizeOpenAIStopReason(choice.FinishReason)
	if err != nil {
		return runtimeapi.CompletionResponse{}, err
	}
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role:    runtimeapi.RoleAssistant,
			Content: content,
		},
		Usage: runtimeapi.Usage{
			InputTokens:     response.Usage.PromptTokens,
			OutputTokens:    response.Usage.CompletionTokens,
			TotalTokens:     response.Usage.TotalTokens,
			ReasoningTokens: response.Usage.CompletionTokenDetails.ReasoningTokens,
		},
		StopReason: stopReason,
		RequestID:  responseRequestID,
	}, nil
}

func normalizeOpenAIStopReason(value string) (runtimeapi.StopReason, error) {
	switch value {
	case "stop":
		return runtimeapi.StopReasonEndTurn, nil
	case "length":
		return runtimeapi.StopReasonMaxTokens, nil
	case "tool_calls":
		return runtimeapi.StopReasonToolUse, nil
	case "content_filter":
		return runtimeapi.StopReasonContentFilter, nil
	case "refusal":
		return runtimeapi.StopReasonRefusal, nil
	default:
		return "", fmt.Errorf("unsupported finish reason %q", value)
	}
}
