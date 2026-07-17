package main

import (
	"testing"
)

func TestParseConfigDefaults(t *testing.T) {
	config, err := parseConfig([]string{"--", "echo", "hello"}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if config.Format != CaptureFormatLastLine {
		t.Fatalf("expected last-line format, got %q", config.Format)
	}
	if config.TerminationPath != defaultTerminationPath {
		t.Fatalf("unexpected termination path %q", config.TerminationPath)
	}
	if config.MaxCaptureBytes != defaultCaptureBytes {
		t.Fatalf("unexpected capture limit %d", config.MaxCaptureBytes)
	}
	if len(config.Command) != 2 || config.Command[0] != "echo" || config.Command[1] != "hello" {
		t.Fatalf("unexpected command %#v", config.Command)
	}
}

func TestParseConfigUsesEnvironment(t *testing.T) {
	values := map[string]string{
		"KONTEXT_RESULT_FORMAT":       "KontextEnvelope",
		"KONTEXT_TERMINATION_MESSAGE": "/tmp/result.json",
	}
	config, err := parseConfig([]string{"--", "agent"}, func(name string) string {
		return values[name]
	})
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if config.Format != CaptureFormatKontextEnvelope {
		t.Fatalf("unexpected format %q", config.Format)
	}
	if config.TerminationPath != "/tmp/result.json" {
		t.Fatalf("unexpected termination path %q", config.TerminationPath)
	}
}

func TestParseConfigRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing command"},
		{name: "unknown format", args: []string{"--format", "json", "--", "agent"}},
		{name: "empty termination path", args: []string{"--termination-log", "", "--", "agent"}},
		{name: "capture limit below termination message", args: []string{"--max-capture-bytes", "100", "--", "agent"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseConfig(test.args, func(string) string { return "" }); err == nil {
				t.Fatalf("expected configuration error")
			}
		})
	}
}
