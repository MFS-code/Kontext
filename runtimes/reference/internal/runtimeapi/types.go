package runtimeapi

import (
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
	ContentTypeText ContentType = "text"
)

type ContentBlock struct {
	Type ContentType
	Text string
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
	StopReasonEndTurn      StopReason = "end_turn"
	StopReasonMaxTokens    StopReason = "max_tokens"
	StopReasonStopSequence StopReason = "stop_sequence"
)

type CompletionRequest struct {
	Model     string
	Messages  []Message
	Tools     []string
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
	if len(response.Message.Content) == 0 {
		return errors.New("assistant response has no content")
	}
	for index, block := range response.Message.Content {
		if block.Type != ContentTypeText {
			return fmt.Errorf("assistant content block %d has unsupported type %q", index, block.Type)
		}
		if strings.TrimSpace(block.Text) == "" {
			return fmt.Errorf("assistant content block %d has empty text", index)
		}
	}
	switch response.StopReason {
	case StopReasonEndTurn, StopReasonMaxTokens, StopReasonStopSequence:
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
