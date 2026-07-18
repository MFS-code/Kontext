package runtimeapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeToolCall   ContentType = "tool_call"
	ContentTypeToolResult ContentType = "tool_result"
)

type ContentBlock struct {
	Type       ContentType
	Text       string
	ToolCall   *ToolCall
	ToolResult *ToolResult
}

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type ToolResult struct {
	CallID    string
	Name      string
	Content   string
	IsError   bool
	ErrorCode string
	Truncated bool
}

type Message struct {
	Role    Role
	Content []ContentBlock
}

type Usage struct {
	InputTokens     *int64
	OutputTokens    *int64
	TotalTokens     *int64
	ReasoningTokens *int64
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
	Tools     []ToolDefinition
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
			if block.ToolCall != nil || block.ToolResult != nil {
				return fmt.Errorf("assistant text block %d contains non-text data", index)
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
			if block.ToolResult != nil {
				return fmt.Errorf("assistant tool-call block %d contains a tool result", index)
			}
		case ContentTypeToolResult:
			return fmt.Errorf("assistant content block %d unexpectedly contains a tool result", index)
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
	if err := validateUsage(response.Usage); err != nil {
		return err
	}
	return nil
}

func validateUsage(usage Usage) error {
	for _, metric := range []struct {
		name  string
		value *int64
	}{
		{name: "input", value: usage.InputTokens},
		{name: "output", value: usage.OutputTokens},
		{name: "total", value: usage.TotalTokens},
	} {
		if metric.value != nil && *metric.value < 0 {
			return fmt.Errorf("%s token usage cannot be negative", metric.name)
		}
	}
	var measuredParts int64
	if usage.InputTokens != nil {
		measuredParts += *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		if measuredParts > math.MaxInt64-*usage.OutputTokens {
			return errors.New("input and output token usage overflow int64")
		}
		measuredParts += *usage.OutputTokens
	}
	if usage.TotalTokens != nil {
		if measuredParts > *usage.TotalTokens {
			return fmt.Errorf(
				"total token usage %d is lower than measured input and output usage %d",
				*usage.TotalTokens,
				measuredParts,
			)
		}
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

func MessageToolResults(message Message) []ToolResult {
	var results []ToolResult
	for _, block := range message.Content {
		if block.Type != ContentTypeToolResult || block.ToolResult == nil {
			continue
		}
		results = append(results, *block.ToolResult)
	}
	return results
}

func CloneToolDefinitions(definitions []ToolDefinition) []ToolDefinition {
	cloned := make([]ToolDefinition, len(definitions))
	for index, definition := range definitions {
		cloned[index] = definition
		cloned[index].InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	}
	return cloned
}
