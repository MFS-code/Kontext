package events

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

const APIVersion = "kontext.dev/event/v1alpha1"

type Type string

const (
	TypeLifecycle Type = "lifecycle"
	TypeOutput    Type = "output"
	TypeUsage     Type = "usage"
	TypeTool      Type = "tool"
	TypeError     Type = "error"
)

type Event struct {
	APIVersion string          `json:"apiVersion"`
	Timestamp  time.Time       `json:"timestamp"`
	Type       Type            `json:"type"`
	Data       json.RawMessage `json:"data"`
}

type Emitter struct {
	writer      io.Writer
	errorWriter io.Writer
	now         func() time.Time
	mu          sync.Mutex
}

func NewEmitter(writer io.Writer, errorWriter io.Writer, now func() time.Time) *Emitter {
	if now == nil {
		now = time.Now
	}
	return &Emitter{writer: writer, errorWriter: errorWriter, now: now}
}

// Emit writes one JSONL event. Events are best-effort observability: the
// result envelope is the contract, so emission failures are logged to the
// error writer instead of failing the run.
func (emitter *Emitter) Emit(eventType Type, data any) {
	encodedData, err := json.Marshal(data)
	if err != nil {
		emitter.logFailure(eventType, err)
		return
	}
	event := Event{
		APIVersion: APIVersion,
		Timestamp:  emitter.now().UTC(),
		Type:       eventType,
		Data:       encodedData,
	}

	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if err := json.NewEncoder(emitter.writer).Encode(event); err != nil {
		emitter.logFailure(eventType, err)
	}
}

func (emitter *Emitter) logFailure(eventType Type, err error) {
	if emitter.errorWriter == nil {
		return
	}
	fmt.Fprintf(emitter.errorWriter, "kontext reference runtime: emit %s event: %v\n", eventType, err)
}
