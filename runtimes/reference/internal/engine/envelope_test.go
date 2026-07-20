package engine_test

import (
	"testing"
	"time"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/engine"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

func TestSuccessPreservesStructuredUsageAndOpaqueModel(t *testing.T) {
	zero := int64(0)
	outputTokens := int64(4)
	reasoningTokens := int64(3)
	response := runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{Type: runtimeapi.ContentTypeText, Text: "answer"},
			},
		},
		Usage: runtimeapi.Usage{
			InputTokens:     &zero,
			OutputTokens:    &outputTokens,
			ReasoningTokens: &reasoningTokens,
		},
		StopReason: runtimeapi.StopReasonEndTurn,
		RequestID:  "request-1",
	}
	started := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	envelope := engine.Success(response, engine.Metadata{
		Provider:    "fake",
		Model:       "vendor/model@2026:beta",
		RequestID:   response.RequestID,
		StartedAt:   started,
		CompletedAt: started.Add(25 * time.Millisecond),
	})

	if err := envelope.Validate(); err != nil {
		t.Fatalf("validate envelope: %v", err)
	}
	if envelope.Execution == nil || envelope.Execution.Model != "vendor/model@2026:beta" {
		t.Fatalf("opaque model changed: %#v", envelope.Execution)
	}
	if envelope.Usage == nil || envelope.Usage.InputTokens == nil ||
		*envelope.Usage.InputTokens != 0 {
		t.Fatalf("measured zero input tokens were lost: %#v", envelope.Usage)
	}
	if envelope.Usage.TotalTokens != nil {
		t.Fatalf("missing total tokens must remain absent: %#v", envelope.Usage)
	}
	if envelope.Usage.ReasoningTokens == nil || *envelope.Usage.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens were lost: %#v", envelope.Usage)
	}
}

func TestFailureBuildsValidatedFailureEnvelope(t *testing.T) {
	started := time.Now().UTC()
	envelope := engine.Failure(
		"provider_error",
		"unavailable",
		nil,
		engine.Metadata{
			Provider:    "fake",
			Model:       "model",
			StartedAt:   started,
			CompletedAt: started,
		},
	)
	if err := envelope.Validate(); err != nil {
		t.Fatalf("validate envelope: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "provider_error" {
		t.Fatalf("unexpected error info %#v", envelope.Error)
	}
}
