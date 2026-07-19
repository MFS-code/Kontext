package v1alpha1_test

import (
	"strings"
	"testing"

	eventv1alpha1 "github.com/kontext-dev/kontext/pkg/event/v1alpha1"
)

func TestParseValidatesVersionTypeTimestampAndShape(t *testing.T) {
	valid := `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"tool","data":{"name":"shell"}}`
	event, err := eventv1alpha1.Parse([]byte(valid))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if event.Type != eventv1alpha1.TypeTool {
		t.Fatalf("unexpected event type %q", event.Type)
	}
	for name, input := range map[string]string{
		"version":   strings.Replace(valid, eventv1alpha1.APIVersion, "other/v1", 1),
		"type":      strings.Replace(valid, `"tool"`, `"unknown"`, 1),
		"timestamp": strings.Replace(valid, `"2026-07-19T00:00:00Z"`, `null`, 1),
		"unknown":   strings.Replace(valid, `"data":`, `"extra":true,"data":`, 1),
		"trailing":  valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := eventv1alpha1.Parse([]byte(input)); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}
