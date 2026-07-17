package conversation_test

import (
	"testing"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/conversation"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

func TestStateTracksConversationWithoutExposingMutableSlices(t *testing.T) {
	state := conversation.New("goal")
	state.Append(runtimeapi.Message{
		Role: runtimeapi.RoleAssistant,
		Content: []runtimeapi.ContentBlock{
			{Type: runtimeapi.ContentTypeText, Text: "answer"},
		},
	})

	messages := state.Messages()
	if len(messages) != 2 || runtimeapi.MessageText(messages[0]) != "goal" {
		t.Fatalf("unexpected conversation %#v", messages)
	}
	messages[0].Content[0].Text = "mutated"
	if got := runtimeapi.MessageText(state.Messages()[0]); got != "goal" {
		t.Fatalf("state was mutated through returned slice: %q", got)
	}
}
