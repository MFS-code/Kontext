package v1alpha1_test

import (
	"encoding/json"
	"strings"
	"testing"

	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

func TestParseVersionedEnvelopeWithArbitraryJSONOutput(t *testing.T) {
	message := `{
		"apiVersion":"kontext.dev/result/v1alpha1",
		"outcome":"Succeeded",
		"output":{"mediaType":"application/json","value":{"answer":42,"items":[true,null]}},
		"usage":{"inputTokens":0,"outputTokens":12,"reasoningTokens":0},
		"extensions":{"anthropic.com/request":{"region":"us-east-1"}}
	}`

	parsed, err := resultv1alpha1.Parse(message)
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if parsed.Envelope == nil || parsed.Legacy {
		t.Fatalf("expected versioned envelope, got %#v", parsed)
	}
	if got := resultv1alpha1.ProjectLegacyResult(parsed.Output); got != `{"answer":42,"items":[true,null]}` {
		t.Fatalf("unexpected legacy projection %q", got)
	}
	if parsed.Usage == nil || parsed.Usage.InputTokens == nil || *parsed.Usage.InputTokens != 0 {
		t.Fatalf("expected measured zero input tokens, got %#v", parsed.Usage)
	}
	if parsed.Usage.OutputTokens == nil || *parsed.Usage.OutputTokens != 12 {
		t.Fatalf("expected 12 output tokens, got %#v", parsed.Usage)
	}
	if parsed.Usage.ReasoningTokens == nil || *parsed.Usage.ReasoningTokens != 0 {
		t.Fatalf("expected measured zero reasoning tokens, got %#v", parsed.Usage)
	}
	if parsed.Usage.TotalTokens != nil || parsed.Usage.Dollars != nil {
		t.Fatalf("missing metrics must remain absent, got %#v", parsed.Usage)
	}
}

func TestParseLegacyPayload(t *testing.T) {
	parsed, err := resultv1alpha1.Parse(`{"result":"done","tokensUsed":0,"dollarsUsed":1.5}`)
	if err != nil {
		t.Fatalf("parse legacy payload: %v", err)
	}
	if !parsed.Legacy || parsed.Envelope != nil {
		t.Fatalf("expected legacy payload, got %#v", parsed)
	}
	if got := resultv1alpha1.ProjectLegacyResult(parsed.Output); got != "done" {
		t.Fatalf("expected done, got %q", got)
	}
	if parsed.Usage == nil || parsed.Usage.TotalTokens == nil || *parsed.Usage.TotalTokens != 0 {
		t.Fatalf("expected measured zero total tokens, got %#v", parsed.Usage)
	}
	if parsed.Usage.Dollars == nil || *parsed.Usage.Dollars != 1.5 {
		t.Fatalf("expected measured dollars, got %#v", parsed.Usage)
	}
}

func TestParseLegacyPayloadWithoutUsageLeavesUsageAbsent(t *testing.T) {
	parsed, err := resultv1alpha1.Parse(`{"result":"done"}`)
	if err != nil {
		t.Fatalf("parse legacy payload: %v", err)
	}
	if parsed.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("expected explicit successful outcome, got %q", parsed.Outcome)
	}
	if parsed.Usage != nil {
		t.Fatalf("expected absent usage, got %#v", parsed.Usage)
	}
}

func TestParseLegacyErrorPreservesExitCodeAuthority(t *testing.T) {
	parsed, err := resultv1alpha1.Parse(`{"result":"done","error":"informational warning"}`)
	if err != nil {
		t.Fatalf("parse legacy payload: %v", err)
	}
	if parsed.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("legacy error must not override a successful process exit, got %q", parsed.Outcome)
	}
	if parsed.Error == nil || parsed.Error.Message != "informational warning" {
		t.Fatalf("expected legacy error details to remain available, got %#v", parsed.Error)
	}
}

func TestParsePlainText(t *testing.T) {
	parsed, err := resultv1alpha1.Parse("plain answer")
	if err != nil {
		t.Fatalf("parse plain text: %v", err)
	}
	if parsed.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("expected explicit successful outcome, got %q", parsed.Outcome)
	}
	if got := resultv1alpha1.ProjectLegacyResult(parsed.Output); got != "plain answer" {
		t.Fatalf("expected plain answer, got %q", got)
	}
}

func TestParseRejectsMalformedAndPartiallyWrittenPayloads(t *testing.T) {
	cases := []string{
		`{"result":"partial",`,
		`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","output":`,
	}
	for _, message := range cases {
		t.Run(message, func(t *testing.T) {
			if _, err := resultv1alpha1.Parse(message); err == nil {
				t.Fatalf("expected malformed payload error")
			}
		})
	}
}

func TestParseAcceptsSuccessfulAndFailedOutcomes(t *testing.T) {
	success, err := resultv1alpha1.Parse(`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded"}`)
	if err != nil || success.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("unexpected success result %#v, error %v", success, err)
	}

	failure, err := resultv1alpha1.Parse(`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Failed","error":{"code":"provider_error","message":"unavailable","retryable":true}}`)
	if err != nil {
		t.Fatalf("parse failed outcome: %v", err)
	}
	if failure.Outcome != resultv1alpha1.OutcomeFailed || failure.Error == nil || failure.Error.Code != "provider_error" {
		t.Fatalf("unexpected failed result %#v", failure)
	}
}

func TestParseRejectsInvalidEnvelope(t *testing.T) {
	cases := []string{
		`{"apiVersion":"kontext.dev/result/v2","outcome":"Succeeded"}`,
		`{"apiVersion":"","outcome":"Succeeded"}`,
		`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Unknown"}`,
		`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Failed"}`,
		`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","output":{"mediaType":"application/json","value":`,
		`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","usage":{"reasoningTokens":-1}}`,
		`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","usage":{"outputTokens":4,"reasoningTokens":5}}`,
	}
	for _, message := range cases {
		t.Run(message, func(t *testing.T) {
			if _, err := resultv1alpha1.Parse(message); err == nil {
				t.Fatalf("expected invalid envelope error")
			}
		})
	}
}

func TestCompactProducesValidBoundedEnvelope(t *testing.T) {
	largeValue, err := json.Marshal(map[string]string{"text": strings.Repeat("x", 8000)})
	if err != nil {
		t.Fatalf("marshal test output: %v", err)
	}
	envelope := resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeSucceeded,
		Output: &resultv1alpha1.Output{
			MediaType: "application/json",
			Value:     largeValue,
		},
		Artifacts: []resultv1alpha1.Artifact{{
			Name: "transcript",
			URI:  "s3://example/transcript",
		}},
		Extensions: map[string]json.RawMessage{
			"example.com/debug": json.RawMessage(`{"trace":"` + strings.Repeat("y", 2000) + `"}`),
		},
	}

	compacted, err := resultv1alpha1.Compact(envelope, resultv1alpha1.MaxTerminationMessageBytes)
	if err != nil {
		t.Fatalf("compact envelope: %v", err)
	}
	if len(compacted) > resultv1alpha1.MaxTerminationMessageBytes {
		t.Fatalf("compacted envelope is %d bytes", len(compacted))
	}
	if !json.Valid(compacted) {
		t.Fatalf("compacted envelope is invalid JSON: %s", compacted)
	}
	parsed, err := resultv1alpha1.Parse(string(compacted))
	if err != nil {
		t.Fatalf("parse compacted envelope: %v", err)
	}
	if parsed.Envelope.Truncation == nil || !parsed.Envelope.Truncation.OutputTruncated {
		t.Fatalf("expected explicit output truncation, got %#v", parsed.Envelope.Truncation)
	}
	marker := resultv1alpha1.TruncatedOutput()
	if parsed.Envelope.Output == nil ||
		parsed.Envelope.Output.MediaType != resultv1alpha1.TruncatedOutputMediaType ||
		parsed.Envelope.Output.MediaType != marker.MediaType ||
		string(parsed.Envelope.Output.Value) != string(marker.Value) {
		t.Fatalf("compaction did not use the shared truncation marker: %#v", parsed.Envelope.Output)
	}
}

func TestCompactLeavesSmallEnvelopeUnchanged(t *testing.T) {
	envelope := resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeSucceeded,
	}
	got, err := resultv1alpha1.Compact(envelope, resultv1alpha1.MaxTerminationMessageBytes)
	if err != nil {
		t.Fatalf("compact envelope: %v", err)
	}
	want, _ := json.Marshal(envelope)
	if string(got) != string(want) {
		t.Fatalf("small envelope changed: got %s want %s", got, want)
	}
}
