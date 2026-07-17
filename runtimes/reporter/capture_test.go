package main

import (
	"strings"
	"testing"
)

func TestCaptureLastNonEmptyLine(t *testing.T) {
	capture := newCapture(CaptureFormatLastLine, 4096)
	_, _ = capture.Write([]byte("first\n\nsecond"))
	_, _ = capture.Write([]byte(" line\n\n"))

	result := capture.Result()
	if !result.Found {
		t.Fatalf("expected captured result")
	}
	if got := string(result.Data); got != "second line" {
		t.Fatalf("expected final non-empty line, got %q", got)
	}
	if result.Truncated {
		t.Fatalf("did not expect truncation")
	}
}

func TestCaptureUsesLastPrefixedEnvelope(t *testing.T) {
	capture := newCapture(CaptureFormatKontextEnvelope, 4096)
	_, _ = capture.Write([]byte(
		"ordinary log\n" +
			`KONTEXT_RESULT: {"apiVersion":"old"}` + "\n" +
			`KONTEXT_RESULT: {"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded"}`,
	))

	result := capture.Result()
	if !result.Found {
		t.Fatalf("expected prefixed result")
	}
	want := `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded"}`
	if got := string(result.Data); got != want {
		t.Fatalf("expected last candidate %q, got %q", want, got)
	}
}

func TestCaptureBoundsLongLines(t *testing.T) {
	capture := newCapture(CaptureFormatLastLine, 4096)
	_, _ = capture.Write([]byte(strings.Repeat("x", 8192)))

	result := capture.Result()
	if !result.Found || !result.Truncated {
		t.Fatalf("expected truncated capture, got %#v", result)
	}
	if len(result.Data) != 4096 {
		t.Fatalf("expected 4096 retained bytes, got %d", len(result.Data))
	}
	if result.OriginalBytes != 8192 {
		t.Fatalf("expected original length 8192, got %d", result.OriginalBytes)
	}
}

func TestCaptureDoesNotTreatOrdinaryLogsAsEnvelope(t *testing.T) {
	capture := newCapture(CaptureFormatKontextEnvelope, 4096)
	_, _ = capture.Write([]byte("RESULT: not the reporter protocol\nordinary log\n"))
	if result := capture.Result(); result.Found {
		t.Fatalf("unexpected result %#v", result)
	}
}
