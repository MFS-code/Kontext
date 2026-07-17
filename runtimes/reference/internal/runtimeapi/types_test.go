package runtimeapi_test

import (
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
