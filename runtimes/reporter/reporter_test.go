package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

func TestEnvelopeFromLastLine(t *testing.T) {
	success := envelopeFromCapture(
		CaptureFormatLastLine,
		CapturedResult{Data: []byte("final answer"), Found: true},
		0,
	)
	if success.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("expected success, got %s", success.Outcome)
	}
	if got := resultv1alpha1.ProjectLegacyResult(success.Output); got != "final answer" {
		t.Fatalf("unexpected output %q", got)
	}

	failure := envelopeFromCapture(
		CaptureFormatLastLine,
		CapturedResult{Data: []byte("partial answer"), Found: true},
		17,
	)
	if failure.Outcome != resultv1alpha1.OutcomeFailed {
		t.Fatalf("expected failure, got %s", failure.Outcome)
	}
	if failure.Error == nil || failure.Error.Code != "agent_process_exit" {
		t.Fatalf("unexpected process error %#v", failure.Error)
	}
	if got := resultv1alpha1.ProjectLegacyResult(failure.Output); got != "partial answer" {
		t.Fatalf("unexpected partial output %q", got)
	}

	empty := envelopeFromCapture(CaptureFormatLastLine, CapturedResult{}, 0)
	if empty.Outcome != resultv1alpha1.OutcomeSucceeded || empty.Output != nil {
		t.Fatalf("expected successful empty output, got %#v", empty)
	}

	truncated := envelopeFromCapture(
		CaptureFormatLastLine,
		CapturedResult{
			Data:          []byte("partial"),
			Found:         true,
			Truncated:     true,
			OriginalBytes: 8192,
		},
		0,
	)
	if truncated.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("expected successful truncated result, got %s", truncated.Outcome)
	}
	if truncated.Truncation == nil || !truncated.Truncation.OutputTruncated ||
		truncated.Truncation.OriginalBytes != 8192 {
		t.Fatalf("expected explicit truncation metadata, got %#v", truncated.Truncation)
	}
	marker := resultv1alpha1.TruncatedOutput()
	if truncated.Output == nil ||
		truncated.Output.MediaType != resultv1alpha1.TruncatedOutputMediaType ||
		truncated.Output.MediaType != marker.MediaType ||
		string(truncated.Output.Value) != string(marker.Value) {
		t.Fatalf("unexpected truncated output %#v", truncated.Output)
	}
}

func TestEnvelopeFromPrefixedCandidate(t *testing.T) {
	valid := CapturedResult{
		Found: true,
		Data: []byte(
			`{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","output":{"mediaType":"application/json","value":{"ok":true}}}`,
		),
	}
	envelope := envelopeFromCapture(CaptureFormatKontextEnvelope, valid, 0)
	if envelope.Outcome != resultv1alpha1.OutcomeSucceeded || envelope.Output == nil {
		t.Fatalf("unexpected envelope %#v", envelope)
	}

	tests := []struct {
		name     string
		captured CapturedResult
	}{
		{name: "missing"},
		{name: "truncated", captured: CapturedResult{Found: true, Truncated: true, Data: []byte(`{}`)}},
		{name: "malformed", captured: CapturedResult{Found: true, Data: []byte(`{"apiVersion":`)}},
		{name: "legacy", captured: CapturedResult{Found: true, Data: []byte(`{"result":"old"}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := envelopeFromCapture(CaptureFormatKontextEnvelope, test.captured, 0)
			if got.Outcome != resultv1alpha1.OutcomeFailed {
				t.Fatalf("expected failed result, got %#v", got)
			}
			if got.Error == nil || (got.Error.Code != "result_missing" && got.Error.Code != "result_invalid") {
				t.Fatalf("unexpected error %#v", got.Error)
			}
		})
	}
}

func TestWriteEnvelopeCompactsOversizedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination.json")
	value, err := json.Marshal(strings.Repeat("x", 16*1024))
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	envelope := resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeSucceeded,
		Output: &resultv1alpha1.Output{
			MediaType: resultv1alpha1.DefaultMediaType,
			Value:     value,
		},
	}
	if err := writeEnvelope(path, envelope); err != nil {
		t.Fatalf("write envelope: %v", err)
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	if len(payload) > resultv1alpha1.MaxTerminationMessageBytes {
		t.Fatalf("termination payload is %d bytes", len(payload))
	}
	var compacted resultv1alpha1.Envelope
	if err := json.Unmarshal(payload, &compacted); err != nil {
		t.Fatalf("decode compacted envelope: %v", err)
	}
	if compacted.Truncation == nil || !compacted.Truncation.OutputTruncated {
		t.Fatalf("expected explicit output truncation, got %#v", compacted.Truncation)
	}
}

func TestRunReporterPreservesLogsAndChildExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := Config{
		Format:          CaptureFormatLastLine,
		TerminationPath: path,
		MaxCaptureBytes: defaultCaptureBytes,
		Command:         []string{"sh", "-c", `printf 'log one\nfinal answer\n'; printf 'warning\n' >&2; exit 7`},
	}

	exitCode := runReporter(context.Background(), config, &stdout, &stderr, nil)
	if exitCode != 7 {
		t.Fatalf("expected child exit 7, got %d", exitCode)
	}
	if stdout.String() != "log one\nfinal answer\n" {
		t.Fatalf("stdout changed: %q", stdout.String())
	}
	if stderr.String() != "warning\n" {
		t.Fatalf("stderr changed: %q", stderr.String())
	}

	envelope := readTestEnvelope(t, path)
	if envelope.Outcome != resultv1alpha1.OutcomeFailed {
		t.Fatalf("expected failed envelope, got %s", envelope.Outcome)
	}
	if envelope.Error == nil || envelope.Error.Code != "agent_process_exit" {
		t.Fatalf("unexpected error %#v", envelope.Error)
	}
	if got := resultv1alpha1.ProjectLegacyResult(envelope.Output); got != "final answer" {
		t.Fatalf("unexpected final output %q", got)
	}
}

func TestRunReporterDistinguishesReporterFailures(t *testing.T) {
	t.Run("child start", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "termination.json")
		var stderr bytes.Buffer
		config := Config{
			Format:          CaptureFormatLastLine,
			TerminationPath: path,
			MaxCaptureBytes: defaultCaptureBytes,
			Command:         []string{"/definitely/missing/agent"},
		}
		if exitCode := runReporter(context.Background(), config, &bytes.Buffer{}, &stderr, nil); exitCode != childStartExitCode {
			t.Fatalf("expected child-start exit %d, got %d", childStartExitCode, exitCode)
		}
		envelope := readTestEnvelope(t, path)
		if envelope.Error == nil || envelope.Error.Code != "child_start_failed" {
			t.Fatalf("unexpected error %#v", envelope.Error)
		}
	})

	t.Run("log forwarding", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "termination.json")
		config := Config{
			Format:          CaptureFormatLastLine,
			TerminationPath: path,
			MaxCaptureBytes: defaultCaptureBytes,
			Command:         []string{"sh", "-c", "printf output"},
		}
		if exitCode := runReporter(context.Background(), config, failingWriter{}, &bytes.Buffer{}, nil); exitCode != reporterFailureExitCode {
			t.Fatalf("expected reporter exit %d, got %d", reporterFailureExitCode, exitCode)
		}
		envelope := readTestEnvelope(t, path)
		if envelope.Error == nil || envelope.Error.Code != "reporter_internal" {
			t.Fatalf("unexpected error %#v", envelope.Error)
		}
	})
}

func readTestEnvelope(t *testing.T, path string) resultv1alpha1.Envelope {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read termination message: %v", err)
	}
	var envelope resultv1alpha1.Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode termination message: %v", err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("validate termination message: %v", err)
	}
	return envelope
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("log sink unavailable")
}
