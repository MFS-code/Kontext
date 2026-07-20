package engine_test

import (
	"bytes"
	"testing"
	"time"

	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/engine"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

func TestSuccessUsesExplicitCumulativeUsageAndOpaqueModel(t *testing.T) {
	zero := int64(0)
	outputTokens := int64(14)
	reasoningTokens := int64(7)
	response := runtimeapi.CompletionResponse{
		Message: runtimeapi.Message{
			Role: runtimeapi.RoleAssistant,
			Content: []runtimeapi.ContentBlock{
				{Type: runtimeapi.ContentTypeText, Text: "answer"},
			},
		},
		StopReason: runtimeapi.StopReasonEndTurn,
		RequestID:  "request-1",
	}
	cumulativeUsage := runtimeapi.Usage{
		InputTokens:     &zero,
		OutputTokens:    &outputTokens,
		ReasoningTokens: &reasoningTokens,
	}
	started := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	envelope := engine.Success(response.Message, cumulativeUsage, engine.Metadata{
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
	if envelope.Usage.OutputTokens == nil || *envelope.Usage.OutputTokens != 14 ||
		envelope.Usage.ReasoningTokens == nil || *envelope.Usage.ReasoningTokens != 7 {
		t.Fatalf("reasoning tokens were lost: %#v", envelope.Usage)
	}
}

func TestEmitFailureBuildsValidatedFailureEnvelope(t *testing.T) {
	started := time.Now().UTC()
	var output bytes.Buffer
	if err := engine.EmitFailure(
		&output,
		nil,
		"provider_error",
		"unavailable",
		nil,
		engine.Metadata{
			Provider:    "fake",
			Model:       "model",
			StartedAt:   started,
			CompletedAt: started,
		},
	); err != nil {
		t.Fatalf("emit failure: %v", err)
	}
	payload, found := resultv1alpha1.ExtractEnvelopePayload(output.Bytes())
	if !found {
		t.Fatalf("failure envelope not found in %q", output.String())
	}
	parsed, err := resultv1alpha1.Parse(string(payload))
	if err != nil || parsed.Envelope == nil {
		t.Fatalf("parse failure envelope: parsed=%#v err=%v", parsed, err)
	}
	envelope := *parsed.Envelope
	if err := envelope.Validate(); err != nil {
		t.Fatalf("validate envelope: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "provider_error" {
		t.Fatalf("unexpected error info %#v", envelope.Error)
	}
}
