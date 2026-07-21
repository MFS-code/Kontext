package engine_test

import (
	"context"
	"encoding/json"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/engine"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/events"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

func baseConfig() config.Config {
	return config.Config{
		RunName:      "run-1",
		AgentName:    "agent-1",
		Goal:         "explain the contract",
		Provider:     "fake",
		Model:        "vendor/model@2026:beta",
		FakeScenario: provider.FakeScenarioSuccess,
	}
}

func configWithTokenBudget(budget int64) config.Config {
	runtimeConfig := baseConfig()
	runtimeConfig.TokenBudget = &budget
	return runtimeConfig
}

func int64Pointer(value int64) *int64 {
	return &value
}

func runnerWithTools(
	selectedProvider provider.Provider,
	executor engine.ToolExecutor,
) engine.Runner {
	return engine.Runner{
		Emitter: &recordingEmitter{},
		Resolve: func(config.Config) (provider.Provider, error) {
			return selectedProvider, nil
		},
		ResolveToolsContext: func(context.Context, config.Config) (engine.ToolExecutor, error) {
			return executor, nil
		},
	}
}

func runnerWithoutTools(emitter engine.Emitter) engine.Runner {
	return engine.Runner{
		Emitter: emitter,
		ResolveToolsContext: func(
			context.Context,
			config.Config,
		) (engine.ToolExecutor, error) {
			return &staticToolExecutor{}, nil
		},
	}
}

func lookupExecutor(result runtimeapi.ToolResult) *staticToolExecutor {
	return &staticToolExecutor{
		definitions: []runtimeapi.ToolDefinition{
			{
				Name:        "lookup",
				Description: "Look up a status.",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		result: result,
	}
}

func toolCallResponse(callID string) runtimeapi.CompletionResponse {
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{
					Type: runtimeapi.ContentTypeToolCall,
					ToolCall: &runtimeapi.ToolCall{
						ID:        callID,
						Name:      "lookup",
						Arguments: json.RawMessage(`{}`),
					},
				},
			},
		},
		StopReason: runtimeapi.StopReasonToolUse,
		RequestID:  "tool-request",
	}
}

func finalResponse(text string) runtimeapi.CompletionResponse {
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{Type: runtimeapi.ContentTypeText, Text: text},
			},
		},
		StopReason: runtimeapi.StopReasonEndTurn,
		RequestID:  "final-request",
	}
}

type recordingEmitter struct {
	types []events.Type
}

type dataRecordingEmitter struct {
	events []recordedEvent
}

type recordedEvent struct {
	eventType events.Type
	data      any
}

type emitterFunc func(events.Type, any)

type scriptedProvider struct {
	responses []runtimeapi.CompletionResponse
	requests  []runtimeapi.CompletionRequest
}

func (provider *scriptedProvider) Name() string {
	return "scripted"
}

func (provider *scriptedProvider) Complete(
	_ context.Context,
	request runtimeapi.CompletionRequest,
) (runtimeapi.CompletionResponse, error) {
	index := len(provider.requests)
	if index >= len(provider.responses) {
		panic("unexpected provider completion")
	}
	provider.requests = append(provider.requests, request)
	return provider.responses[index], nil
}

type staticToolExecutor struct {
	definitions []runtimeapi.ToolDefinition
	result      runtimeapi.ToolResult
	err         error
	calls       []runtimeapi.ToolCall
}

type closingToolExecutor struct {
	staticToolExecutor
	close      func(context.Context) error
	closeCalls int
}

func (executor *closingToolExecutor) Close(ctx context.Context) error {
	executor.closeCalls++
	return executor.close(ctx)
}

func (executor *staticToolExecutor) Definitions() []runtimeapi.ToolDefinition {
	return runtimeapi.CloneToolDefinitions(executor.definitions)
}

func (executor *staticToolExecutor) Execute(
	_ context.Context,
	call runtimeapi.ToolCall,
) (runtimeapi.ToolResult, error) {
	executor.calls = append(executor.calls, call)
	result := executor.result
	result.CallID = call.ID
	result.Name = call.Name
	return result, executor.err
}

func (executor *staticToolExecutor) Close(context.Context) error {
	return nil
}

func (emitter *recordingEmitter) Emit(eventType events.Type, _ any) {
	emitter.types = append(emitter.types, eventType)
}

func (emit emitterFunc) Emit(eventType events.Type, data any) {
	emit(eventType, data)
}

func (emitter *dataRecordingEmitter) Emit(eventType events.Type, data any) {
	emitter.events = append(emitter.events, recordedEvent{
		eventType: eventType,
		data:      data,
	})
}

func (emitter *dataRecordingEmitter) first(eventType events.Type) map[string]any {
	for _, event := range emitter.events {
		if event.eventType == eventType {
			data, _ := event.data.(map[string]any)
			return data
		}
	}
	return nil
}

func (emitter *recordingEmitter) has(eventType events.Type) bool {
	return emitter.count(eventType) > 0
}

func (emitter *recordingEmitter) count(eventType events.Type) int {
	var count int
	for _, candidate := range emitter.types {
		if candidate == eventType {
			count++
		}
	}
	return count
}
