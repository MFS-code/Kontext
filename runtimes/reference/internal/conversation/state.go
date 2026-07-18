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
	cloned.Content = make([]runtimeapi.ContentBlock, len(message.Content))
	for index, block := range message.Content {
		cloned.Content[index] = block
		if block.ToolCall != nil {
			call := *block.ToolCall
			call.Arguments = append([]byte(nil), block.ToolCall.Arguments...)
			cloned.Content[index].ToolCall = &call
		}
		if block.ToolResult != nil {
			result := *block.ToolResult
			cloned.Content[index].ToolResult = &result
		}
	}
	return cloned
}
