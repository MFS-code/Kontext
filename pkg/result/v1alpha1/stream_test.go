package v1alpha1_test

import (
	"bytes"
	"strings"
	"testing"

	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

func TestWriteEnvelopeLineRoundTripsThroughExtract(t *testing.T) {
	envelope := resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeFailed,
		Error: &resultv1alpha1.ErrorInfo{
			Code:    "provider_error",
			Message: "unavailable",
		},
	}
	var output bytes.Buffer
	if err := resultv1alpha1.WriteEnvelopeLine(&output, envelope); err != nil {
		t.Fatalf("write envelope line: %v", err)
	}
	line := strings.TrimSuffix(output.String(), "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("expected a single line, got %q", output.String())
	}

	payload, found := resultv1alpha1.ExtractEnvelopePayload([]byte(line))
	if !found {
		t.Fatalf("payload not recognized in %q", line)
	}
	parsed, legacy, err := resultv1alpha1.Parse(string(payload))
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if legacy || parsed.Outcome != resultv1alpha1.OutcomeFailed {
		t.Fatalf("unexpected parsed result %#v", parsed)
	}
}

func TestExtractEnvelopePayloadIgnoresOrdinaryLines(t *testing.T) {
	for _, line := range []string{"", "   ", "plain output", `{"apiVersion":"x"}`} {
		if _, found := resultv1alpha1.ExtractEnvelopePayload([]byte(line)); found {
			t.Fatalf("line %q must not be treated as an envelope", line)
		}
	}
	payload, found := resultv1alpha1.ExtractEnvelopePayload(
		[]byte("  " + resultv1alpha1.EnvelopeLinePrefix + "  {} \r"),
	)
	if !found || string(payload) != "{}" {
		t.Fatalf("expected trimmed payload {}, got %q found=%v", payload, found)
	}
}
