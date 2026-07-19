// Package v1alpha1 defines the versioned JSONL event contract shared by
// Kontext runtimes and external observers.
package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

func (event Event) Validate() error {
	if event.APIVersion != APIVersion {
		return fmt.Errorf("unsupported event apiVersion %q", event.APIVersion)
	}
	switch event.Type {
	case TypeLifecycle, TypeOutput, TypeUsage, TypeTool, TypeError:
	default:
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
	if event.Timestamp.IsZero() {
		return fmt.Errorf("event timestamp is required")
	}
	if len(bytes.TrimSpace(event.Data)) == 0 || !json.Valid(event.Data) {
		return fmt.Errorf("event data must be valid JSON")
	}
	return nil
}

func Parse(line []byte) (Event, error) {
	var event Event
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Event{}, fmt.Errorf("decode event: trailing JSON value")
		}
		return Event{}, fmt.Errorf("decode event trailing data: %w", err)
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}
