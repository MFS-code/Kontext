package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
)

func TestRunEmitsJSONLAndVersionedResult(t *testing.T) {
	values := map[string]string{
		"KONTEXT_RUN_NAME": "reference-test",
		"KONTEXT_GOAL":     "explain the contract",
		"KONTEXT_PROVIDER": "fake",
		"KONTEXT_MODEL":    "opaque/model:id",
		"KONTEXT_TOOLS":    "declared-only",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		lookupEnv(values),
		&stdout,
		&stderr,
		func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) },
	)
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"apiVersion":"kontext.dev/event/v1alpha1"`) {
		t.Fatalf("expected JSONL events: %s", stdout.String())
	}
	envelope := resultLine(t, stdout.String())
	if envelope.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("unexpected result %#v", envelope)
	}
	if envelope.Execution == nil || envelope.Execution.Model != "opaque/model:id" {
		t.Fatalf("opaque model was not preserved: %#v", envelope.Execution)
	}
}

func TestRunEmitsFailureEnvelopeForInvalidConfiguration(t *testing.T) {
	values := map[string]string{
		"KONTEXT_GOAL":     "goal",
		"KONTEXT_PROVIDER": "fake",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		lookupEnv(values),
		&stdout,
		&stderr,
		time.Now,
	)
	if exitCode == 0 {
		t.Fatalf("expected configuration failure")
	}
	envelope := resultLine(t, stdout.String())
	if envelope.Error == nil || envelope.Error.Code != "invalid_configuration" {
		t.Fatalf("unexpected failure %#v", envelope.Error)
	}
	if !strings.Contains(stderr.String(), "KONTEXT_MODEL is required") {
		t.Fatalf("expected actionable stderr, got %q", stderr.String())
	}
}

func resultLine(t *testing.T, output string) resultv1alpha1.Envelope {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, resultv1alpha1.EnvelopeLinePrefix+" ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, resultv1alpha1.EnvelopeLinePrefix))
		parsed, err := resultv1alpha1.Parse(payload)
		if err != nil {
			t.Fatalf("parse result line: %v", err)
		}
		if parsed.Envelope == nil {
			t.Fatalf("result line was not a versioned envelope")
		}
		return *parsed.Envelope
	}
	t.Fatalf("result line not found in %q", output)
	return resultv1alpha1.Envelope{}
}

func lookupEnv(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}
