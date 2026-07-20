package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

func parseFakeToolSequence(value string) ([]FakeToolSequenceEntry, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("KONTEXT_FAKE_TOOL_SEQUENCE is required for tool_sequence scenario")
	}
	var sequence []FakeToolSequenceEntry
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sequence); err != nil {
		return nil, fmt.Errorf("KONTEXT_FAKE_TOOL_SEQUENCE must contain a JSON array of tool calls: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("KONTEXT_FAKE_TOOL_SEQUENCE must contain exactly one JSON value")
	}
	if len(sequence) == 0 {
		return nil, errors.New("KONTEXT_FAKE_TOOL_SEQUENCE must contain at least one tool call")
	}
	for index := range sequence {
		entry := &sequence[index]
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" {
			return nil, fmt.Errorf("KONTEXT_FAKE_TOOL_SEQUENCE entry %d requires name", index)
		}
		if len(entry.Arguments) == 0 {
			return nil, fmt.Errorf("KONTEXT_FAKE_TOOL_SEQUENCE entry %d requires arguments", index)
		}
		var arguments map[string]json.RawMessage
		if err := json.Unmarshal(entry.Arguments, &arguments); err != nil || arguments == nil {
			return nil, fmt.Errorf(
				"KONTEXT_FAKE_TOOL_SEQUENCE entry %d arguments must be a JSON object",
				index,
			)
		}
	}
	return sequence, nil
}

func fakeToolSequenceCompletion(
	request runtimeapi.CompletionRequest,
	sequence []FakeToolSequenceEntry,
) (runtimeapi.CompletionResponse, error) {
	var results []runtimeapi.ToolResult
	var calls []runtimeapi.ToolCall
	pendingCalls := make(map[string]runtimeapi.ToolCall)
	for _, message := range request.Messages {
		for _, call := range runtimeapi.MessageToolCalls(message) {
			pendingCalls[call.ID] = call
		}
		for _, result := range runtimeapi.MessageToolResults(message) {
			call, exists := pendingCalls[result.CallID]
			if !exists {
				return runtimeapi.CompletionResponse{}, fakeSequenceError(
					"fake_tool_sequence_mismatch",
					"fake tool sequence result has no preceding corresponding assistant call",
				)
			}
			delete(pendingCalls, result.CallID)
			calls = append(calls, call)
			results = append(results, result)
		}
	}
	if len(results) > len(sequence) {
		return runtimeapi.CompletionResponse{}, fakeSequenceError(
			"fake_tool_sequence_mismatch",
			"fake tool sequence received more results than configured calls",
		)
	}
	if len(pendingCalls) != 0 || len(calls) != len(results) {
		return runtimeapi.CompletionResponse{}, fakeSequenceError(
			"fake_tool_sequence_mismatch",
			"fake tool sequence results do not have corresponding assistant calls",
		)
	}
	for index := range results {
		expectedCallID := fmt.Sprintf("fake-tool-call-%03d", index+1)
		if results[index].CallID != expectedCallID ||
			calls[index].ID != expectedCallID ||
			results[index].CallID != calls[index].ID {
			return runtimeapi.CompletionResponse{}, fakeSequenceError(
				"fake_tool_sequence_mismatch",
				fmt.Sprintf(
					"fake tool sequence result %d did not match deterministic call %q",
					index,
					expectedCallID,
				),
			)
		}
		if results[index].Name != sequence[index].Name ||
			calls[index].Name != sequence[index].Name ||
			!bytes.Equal(calls[index].Arguments, sequence[index].Arguments) {
			return runtimeapi.CompletionResponse{}, fakeSequenceError(
				"fake_tool_sequence_mismatch",
				fmt.Sprintf(
					"fake tool sequence call %d did not match configured tool %q",
					index,
					sequence[index].Name,
				),
			)
		}
	}
	if len(results) == len(sequence) {
		type resultSummary struct {
			Name      string `json:"name"`
			Content   string `json:"content"`
			IsError   bool   `json:"isError"`
			ErrorCode string `json:"errorCode,omitempty"`
			Truncated bool   `json:"truncated,omitempty"`
		}
		summaries := make([]resultSummary, 0, len(results))
		for _, result := range results {
			summaries = append(summaries, resultSummary{
				Name:      result.Name,
				Content:   result.Content,
				IsError:   result.IsError,
				ErrorCode: result.ErrorCode,
				Truncated: result.Truncated,
			})
		}
		encoded, _ := json.Marshal(summaries)
		return fakeTextResponse(
			request.Model,
			"Fake provider completed tool sequence: "+string(encoded),
			int64(len(strings.Fields(fakeInputText(request.Messages)))),
		), nil
	}

	next := sequence[len(results)]
	if !toolIsExposed(request.Tools, next.Name) {
		return runtimeapi.CompletionResponse{}, fakeSequenceError(
			"fake_tool_unavailable",
			fmt.Sprintf("fake tool sequence tool %q was not exposed", next.Name),
		)
	}
	index := len(results) + 1
	requestHash := sha256.Sum256([]byte(
		fmt.Sprintf("%s\x00%d\x00%s\x00%s", request.Model, index, next.Name, next.Arguments),
	))
	inputTokens := int64(len(strings.Fields(fakeInputText(request.Messages))))
	outputTokens := int64(len(strings.Fields(next.Name + " " + string(next.Arguments))))
	totalTokens := inputTokens + outputTokens
	return runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{{
				Type: runtimeapi.ContentTypeToolCall,
				ToolCall: &runtimeapi.ToolCall{
					ID:        fmt.Sprintf("fake-tool-call-%03d", index),
					Name:      next.Name,
					Arguments: append(json.RawMessage(nil), next.Arguments...),
				},
			}},
		},
		Usage: runtimeapi.Usage{
			InputTokens:  &inputTokens,
			OutputTokens: &outputTokens,
			TotalTokens:  &totalTokens,
		},
		StopReason: runtimeapi.StopReasonToolUse,
		RequestID:  fmt.Sprintf("fake-%x", requestHash[:6]),
	}, nil
}

func toolIsExposed(definitions []runtimeapi.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func fakeSequenceError(code string, message string) error {
	retryable := false
	return &runtimeapi.ProviderError{
		Code:      code,
		Message:   message,
		Retryable: &retryable,
	}
}
