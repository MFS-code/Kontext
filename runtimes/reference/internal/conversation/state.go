package conversation

import (
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

type State struct {
	messages []runtimeapi.Message
}

func New(goal string) *State {
	return &State{
		messages: []runtimeapi.Message{
			{
				Role: runtimeapi.RoleUser,
				Content: []runtimeapi.ContentBlock{
					{Type: runtimeapi.ContentTypeText, Text: goal},
				},
			},
		},
	}
}

func (state *State) Append(message runtimeapi.Message) {
	state.messages = append(state.messages, cloneMessage(message))
}

func (state *State) Messages() []runtimeapi.Message {
	messages := make([]runtimeapi.Message, len(state.messages))
	for index, message := range state.messages {
		messages[index] = cloneMessage(message)
	}
	return messages
}

func cloneMessage(message runtimeapi.Message) runtimeapi.Message {
	cloned := message
	cloned.Content = append([]runtimeapi.ContentBlock(nil), message.Content...)
	return cloned
}
