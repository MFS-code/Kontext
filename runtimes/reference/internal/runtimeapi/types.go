package runtimeapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type ContentType string

const (
	ContentTypeText     ContentType = "text"
	ContentTypeToolCall ContentType = "tool_call"
)

type ContentBlock struct {
	Type     ContentType
	Text     string
	ToolCall *ToolCall
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type Message struct {
	Role    Role
	Content []ContentBlock
}

type Usage struct {
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
}

type StopReason string

const (
	StopReasonEndTurn                    StopReason = "end_turn"
	StopReasonMaxTokens                  StopReason = "max_tokens"
	StopReasonStopSequence               StopReason = "stop_sequence"
	StopReasonToolUse                    StopReason = "tool_use"
	StopReasonContentFilter              StopReason = "content_filter"
	StopReasonPauseTurn                  StopReason = "pause_turn"
	StopReasonRefusal                    StopReason = "refusal"
	StopReasonModelContextWindowExceeded StopReason = "model_context_window_exceeded"
)

type CompletionRequest struct {
	Model     string
	Messages  []Message
	MaxTokens *int64
}

type CompletionResponse struct {
	Message    Message
	Usage      Usage
	StopReason StopReason
	RequestID  string
}

type ProviderError struct {
	Code       string
	Message    string
	Retryable  *bool
	HTTPStatus *int
	RequestID  string
}

func (err *ProviderError) Error() string {
	if err.Code == "" {
		return err.Message
	}
	return fmt.Sprintf("%s: %s", err.Code, err.Message)
}

func ValidateResponse(response CompletionResponse) error {
	if response.Message.Role != RoleAssistant {
		return fmt.Errorf("assistant response has invalid role %q", response.Message.Role)
	}
	if len(response.Message.Content) == 0 &&
		response.StopReason != StopReasonContentFilter &&
		response.StopReason != StopReasonRefusal {
		return errors.New("assistant response has no content")
	}
	for index, block := range response.Message.Content {
		switch block.Type {
		case ContentTypeText:
			if strings.TrimSpace(block.Text) == "" {
				return fmt.Errorf("assistant content block %d has empty text", index)
			}
			if block.ToolCall != nil {
				return fmt.Errorf("assistant text block %d unexpectedly contains a tool call", index)
			}
		case ContentTypeToolCall:
			if block.ToolCall == nil {
				return fmt.Errorf("assistant tool-call block %d has no tool call", index)
			}
			if strings.TrimSpace(block.ToolCall.ID) == "" {
				return fmt.Errorf("assistant tool-call block %d has no id", index)
			}
			if strings.TrimSpace(block.ToolCall.Name) == "" {
				return fmt.Errorf("assistant tool-call block %d has no name", index)
			}
			if len(block.ToolCall.Arguments) == 0 || !json.Valid(block.ToolCall.Arguments) {
				return fmt.Errorf("assistant tool-call block %d has invalid arguments", index)
			}
			if block.Text != "" {
				return fmt.Errorf("assistant tool-call block %d unexpectedly contains text", index)
			}
		default:
			return fmt.Errorf("assistant content block %d has unsupported type %q", index, block.Type)
		}
	}
	switch response.StopReason {
	case StopReasonEndTurn,
		StopReasonMaxTokens,
		StopReasonStopSequence,
		StopReasonToolUse,
		StopReasonContentFilter,
		StopReasonPauseTurn,
		StopReasonRefusal,
		StopReasonModelContextWindowExceeded:
	default:
		return fmt.Errorf("assistant response has unsupported stop reason %q", response.StopReason)
	}
	return nil
}

func MessageText(message Message) string {
	var parts []string
	for _, block := range message.Content {
		if block.Type == ContentTypeText && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func MessageToolCalls(message Message) []ToolCall {
	var calls []ToolCall
	for _, block := range message.Content {
		if block.Type != ContentTypeToolCall || block.ToolCall == nil {
			continue
		}
		call := *block.ToolCall
		call.Arguments = append(json.RawMessage(nil), block.ToolCall.Arguments...)
		calls = append(calls, call)
	}
	return calls
}
