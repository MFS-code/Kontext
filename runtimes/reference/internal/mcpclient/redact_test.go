package mcpclient

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactorDetectsRawAndJSONEscapedSensitiveValues(t *testing.T) {
	const secret = "quote\"\nsecret"
	redactor := newRedactor([]string{secret})
	if !redactor.containsSensitive("raw=" + secret) {
		t.Fatal("raw sensitive value was not detected")
	}
	if !redactor.containsSensitive(`schema="quote\"\nsecret"`) {
		t.Fatal("JSON-escaped sensitive value was not detected")
	}
	if redactor.containsSensitive("safe") {
		t.Fatal("unrelated value was marked sensitive")
	}
}

func TestRedactingLineWriterBoundsBeforeWriting(t *testing.T) {
	var output bytes.Buffer
	writer := newRedactingLineWriter(&output, newRedactor([]string{"secret"}))
	line := strings.Repeat("x", maxStderrLineBytes) + "secret\n"
	if count, err := writer.Write([]byte(line)); err != nil || count != len(line) {
		t.Fatalf("write = (%d, %v), want (%d, nil)", count, err, len(line))
	}
	if output.String() != "MCP stderr line omitted: too long\n" {
		t.Fatalf("unexpected bounded stderr output: %q", output.String())
	}
}
