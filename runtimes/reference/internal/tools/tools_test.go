package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/tools"
)

func TestRegistryExposesOnlyAllowedTools(t *testing.T) {
	registry, err := tools.New(tools.Config{
		Allowed: []string{tools.NameReadKnowledge, tools.NameReadKnowledge},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 1 || definitions[0].Name != tools.NameReadKnowledge {
		t.Fatalf("unexpected definitions %#v", definitions)
	}
}

func TestRegistryRejectsUnknownConfiguredTool(t *testing.T) {
	_, err := tools.New(tools.Config{Allowed: []string{"not-built-in"}})
	var toolError *tools.Error
	if !errors.As(err, &toolError) || toolError.Code != "unknown_tool" {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestRegistryReturnsDeniedAndUnknownCallsToModel(t *testing.T) {
	registry, err := tools.New(tools.Config{Allowed: []string{tools.NameReadKnowledge}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	tests := []struct {
		name string
		call runtimeapi.ToolCall
		code string
	}{
		{
			name: "known but denied",
			call: runtimeapi.ToolCall{
				ID:        "call-1",
				Name:      tools.NameShell,
				Arguments: json.RawMessage(`{}`),
			},
			code: "tool_denied",
		},
		{
			name: "unknown",
			call: runtimeapi.ToolCall{
				ID:        "call-2",
				Name:      "invented",
				Arguments: json.RawMessage(`{}`),
			},
			code: "unknown_tool",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), test.call)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !result.IsError || result.ErrorCode != test.code {
				t.Fatalf("unexpected result %#v", result)
			}
			if result.CallID != test.call.ID || result.Name != test.call.Name {
				t.Fatalf("call identity changed: %#v", result)
			}
		})
	}
}
