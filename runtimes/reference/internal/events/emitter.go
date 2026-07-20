package events

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
)

const APIVersion = eventv1alpha1.APIVersion

type Type = eventv1alpha1.Type
type Event = eventv1alpha1.Event

const (
	TypeLifecycle = eventv1alpha1.TypeLifecycle
	TypeOutput    = eventv1alpha1.TypeOutput
	TypeUsage     = eventv1alpha1.TypeUsage
	TypeTool      = eventv1alpha1.TypeTool
	TypeError     = eventv1alpha1.TypeError
)

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
