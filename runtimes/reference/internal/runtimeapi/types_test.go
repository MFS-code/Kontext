package runtimeapi_test

import (
	"encoding/json"
	"math"
	"testing"

	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

func TestValidateResponse(t *testing.T) {
	valid := runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{Type: runtimeapi.ContentTypeText, Text: "answer"},
			},
		},
		StopReason: runtimeapi.StopReasonEndTurn,
	}
	if err := runtimeapi.ValidateResponse(valid); err != nil {
		t.Fatalf("validate response: %v", err)
	}

	tests := []struct {
		name   string
		change func(*runtimeapi.CompletionResponse)
	}{
		{name: "wrong role", change: func(response *runtimeapi.CompletionResponse) {
			response.Message.Role = runtimeapi.RoleUser
		}},
		{name: "no content", change: func(response *runtimeapi.CompletionResponse) {
			response.Message.Content = nil
		}},
		{name: "empty text", change: func(response *runtimeapi.CompletionResponse) {
			response.Message.Content[0].Text = ""
		}},
		{name: "unknown content", change: func(response *runtimeapi.CompletionResponse) {
			response.Message.Content[0].Type = runtimeapi.ContentType("image")
		}},
		{name: "unknown stop reason", change: func(response *runtimeapi.CompletionResponse) {
			response.StopReason = runtimeapi.StopReason("unknown")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := valid
			response.Message.Content = append(
				[]runtimeapi.ContentBlock(nil),
				valid.Message.Content...,
			)
			test.change(&response)
			if err := runtimeapi.ValidateResponse(response); err == nil {
				t.Fatalf("expected invalid response")
			}
		})
	}
}

func TestValidateResponseAcceptsToolCallsAndFilteredEmptyContent(t *testing.T) {
	toolCall := runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
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
		StopReason: runtimeapi.StopReasonToolUse,
	}
	if err := runtimeapi.ValidateResponse(toolCall); err != nil {
		t.Fatalf("validate tool call: %v", err)
	}

	filtered := runtimeapi.CompletionResponse{
		Message:    runtimeapi.Message{Role: runtimeapi.RoleAssistant},
		StopReason: runtimeapi.StopReasonContentFilter,
	}
	if err := runtimeapi.ValidateResponse(filtered); err != nil {
		t.Fatalf("validate filtered response: %v", err)
	}
}

func TestValidateResponseRejectsMalformedToolCall(t *testing.T) {
	response := runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{
					Type: runtimeapi.ContentTypeToolCall,
					ToolCall: &runtimeapi.ToolCall{
						ID:        "call-1",
						Name:      "lookup",
						Arguments: json.RawMessage(`{`),
					},
				},
			},
		},
		StopReason: runtimeapi.StopReasonToolUse,
	}
	if err := runtimeapi.ValidateResponse(response); err == nil {
		t.Fatal("expected invalid tool arguments")
	}
}

func TestValidateResponseRejectsInvalidUsage(t *testing.T) {
	base := runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{Type: runtimeapi.ContentTypeText, Text: "answer"},
			},
		},
		StopReason: runtimeapi.StopReasonEndTurn,
	}
	negative := int64(-1)
	base.Usage.InputTokens = &negative
	if err := runtimeapi.ValidateResponse(base); err == nil {
		t.Fatal("expected negative usage rejection")
	}

	input := int64(4)
	output := int64(3)
	total := int64(5)
	base.Usage = runtimeapi.Usage{
		InputTokens:  &input,
		OutputTokens: &output,
		TotalTokens:  &total,
	}
	if err := runtimeapi.ValidateResponse(base); err == nil {
		t.Fatal("expected inconsistent total usage rejection")
	}

	maximum := int64(math.MaxInt64)
	one := int64(1)
	base.Usage = runtimeapi.Usage{
		InputTokens:  &maximum,
		OutputTokens: &one,
	}
	if err := runtimeapi.ValidateResponse(base); err == nil {
		t.Fatal("expected usage overflow rejection")
	}

	base.Usage = runtimeapi.Usage{ReasoningTokens: &negative}
	if err := runtimeapi.ValidateResponse(base); err == nil {
		t.Fatal("expected negative reasoning usage rejection")
	}

	reasoning := int64(4)
	base.Usage = runtimeapi.Usage{
		OutputTokens:    &output,
		ReasoningTokens: &reasoning,
	}
	if err := runtimeapi.ValidateResponse(base); err == nil {
		t.Fatal("expected reasoning usage above output usage rejection")
	}
}

func TestMessageTextJoinsTextBlocks(t *testing.T) {
	message := runtimeapi.Message{
		Role: runtimeapi.RoleAssistant,
		Content: []runtimeapi.ContentBlock{
			{Type: runtimeapi.ContentTypeText, Text: "first"},
			{Type: runtimeapi.ContentTypeText, Text: "second"},
		},
	}
	if got := runtimeapi.MessageText(message); got != "first\nsecond" {
		t.Fatalf("unexpected text %q", got)
	}
}

func TestMessageToolCallsReturnsIndependentArguments(t *testing.T) {
	message := runtimeapi.Message{
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
	}
	calls := runtimeapi.MessageToolCalls(message)
	if len(calls) != 1 || calls[0].Name != "lookup" {
		t.Fatalf("unexpected tool calls %#v", calls)
	}
	calls[0].Arguments[0] = '['
	if string(message.Content[0].ToolCall.Arguments) != `{"query":"status"}` {
		t.Fatal("tool-call arguments were not cloned")
	}
}
