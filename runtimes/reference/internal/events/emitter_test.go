package events_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/events"
)

func TestEmitterWritesOneVersionedJSONEventPerLine(t *testing.T) {
	var output bytes.Buffer
	timestamp := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.FixedZone("test", 3600))
	emitter := events.NewEmitter(&output, nil, func() time.Time { return timestamp })

	emitter.Emit(events.TypeLifecycle, map[string]string{"phase": "started"})
	if lines := strings.Count(output.String(), "\n"); lines != 1 {
		t.Fatalf("expected one JSONL line, got %d: %q", lines, output.String())
	}
	var event events.Event
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.APIVersion != events.APIVersion || event.Type != events.TypeLifecycle {
		t.Fatalf("unexpected event %#v", event)
	}
	if !event.Timestamp.Equal(timestamp.UTC()) {
		t.Fatalf("expected UTC timestamp %s, got %s", timestamp.UTC(), event.Timestamp)
	}
}

func TestEmitterLogsFailuresInsteadOfPropagating(t *testing.T) {
	var errorOutput bytes.Buffer
	emitter := events.NewEmitter(failingWriter{}, &errorOutput, nil)

	emitter.Emit(events.TypeLifecycle, map[string]string{"phase": "started"})

	if !strings.Contains(errorOutput.String(), "emit lifecycle event") {
		t.Fatalf("expected logged emit failure, got %q", errorOutput.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("stream closed")
}
