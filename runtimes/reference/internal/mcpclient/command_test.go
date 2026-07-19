package mcpclient

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLineValidatingReaderServesMultipleValidatedFrames(t *testing.T) {
	const input = "{\"first\":1}\n\n{\"second\":2}"
	reader := newLineValidatingReadCloser(
		io.NopCloser(strings.NewReader(input)),
		maxStdioFrameBytes,
	)
	if count, err := reader.Read(make([]byte, 0)); count != 0 || err != nil {
		t.Fatalf("zero-length read = (%d, %v)", count, err)
	}
	var output strings.Builder
	buffer := make([]byte, 3)
	for {
		count, err := reader.Read(buffer)
		output.Write(buffer[:count])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read valid frames: %v", err)
		}
	}
	if output.String() != input {
		t.Fatalf("frames changed: got %q want %q", output.String(), input)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestLineValidatingReaderRejectsMultilineJSON(t *testing.T) {
	reader := newLineValidatingReadCloser(
		io.NopCloser(strings.NewReader("{\"split\":\ntrue}\n")),
		maxStdioFrameBytes,
	)
	buffer := make([]byte, 64)
	count, err := reader.Read(buffer)
	if count != 0 || !errors.Is(err, errMCPStdioInvalidFrame) {
		t.Fatalf("multiline frame read = (%d, %v)", count, err)
	}
}

func TestLineValidatingReaderRejectsOversizedPhysicalLine(t *testing.T) {
	reader := newLineValidatingReadCloser(
		io.NopCloser(strings.NewReader(`{"value":"oversized"}`+"\n")),
		8,
	)
	buffer := make([]byte, 64)
	count, err := reader.Read(buffer)
	if count != 0 || !errors.Is(err, errMCPStdioFrameLimit) {
		t.Fatalf("oversized frame read = (%d, %v)", count, err)
	}
}

func TestLineValidatingReaderAllowsManyIndependentFramesOverTotalLimit(t *testing.T) {
	const frameCount = 17
	frame := `"` + strings.Repeat("x", 4<<20) + "\"\n"
	sources := make([]io.Reader, frameCount)
	for index := range sources {
		sources[index] = strings.NewReader(frame)
	}
	reader := newLineValidatingReadCloser(
		io.NopCloser(io.MultiReader(sources...)),
		maxStdioFrameBytes,
	)
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		count, err := reader.Read(buffer)
		total += int64(count)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read independent frames after %d bytes: %v", total, err)
		}
	}
	if total <= maxStdioFrameBytes {
		t.Fatalf("test did not cross cumulative frame limit: %d", total)
	}
	expected := int64(len(frame) * frameCount)
	if total != expected {
		t.Fatalf("unexpected bytes: got %d want %d", total, expected)
	}
}
