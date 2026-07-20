package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/tools"
)

func TestShellUsesExplicitDirectoryAndFilteredEnvironment(t *testing.T) {
	var streamed bytes.Buffer
	registry, err := tools.New(tools.Config{
		Allowed: []string{tools.NameShell},
		Stdout:  &streamed,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	workingDirectory := t.TempDir()
	arguments, _ := json.Marshal(map[string]any{
		"command":           `printf '%s|%s|%s' "$VISIBLE" "${OPENAI_API_KEY-unset}" "$PWD"`,
		"working_directory": workingDirectory,
		"environment": map[string]string{
			"VISIBLE": "yes",
		},
	})
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "shell-1",
		Name:      tools.NameShell,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var output struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	resolvedWorkingDirectory, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	expected := "yes|unset|" + resolvedWorkingDirectory
	if result.IsError || output.ExitCode != 0 || output.Stdout != expected {
		t.Fatalf("unexpected result %#v output=%#v", result, output)
	}
	if streamed.String() != expected+"\n" {
		t.Fatalf("stdout was not streamed: %q", streamed.String())
	}
}

func TestShellRejectsSensitiveEnvironmentAndRelativeDirectory(t *testing.T) {
	registry, err := tools.New(tools.Config{Allowed: []string{tools.NameShell}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	tests := map[string]json.RawMessage{
		"sensitive environment": json.RawMessage(`{
			"command":"true",
			"working_directory":"/tmp",
			"environment":{"OPENAI_API_KEY":"value"}
		}`),
		"relative directory": json.RawMessage(`{
			"command":"true",
			"working_directory":"relative"
		}`),
	}
	for name, arguments := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
				ID:        "shell-denied",
				Name:      tools.NameShell,
				Arguments: arguments,
			})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !result.IsError {
				t.Fatalf("expected tool error, got %#v", result)
			}
		})
	}
}

func TestShellBoundsCapturedOutputButStreamsFullLogs(t *testing.T) {
	var streamed bytes.Buffer
	registry, err := tools.New(tools.Config{
		Allowed:          []string{tools.NameShell},
		MaxCapturedBytes: 4,
		Stdout:           &streamed,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:   "shell-bounded",
		Name: tools.NameShell,
		Arguments: json.RawMessage(`{
			"command":"printf 123456789",
			"working_directory":"/tmp"
		}`),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var output struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.Stdout != "1234" || !result.Truncated {
		t.Fatalf("unexpected bounded result %#v output=%#v", result, output)
	}
	if streamed.String() != "123456789\n" {
		t.Fatalf("streamed logs were truncated: %q", streamed.String())
	}
}

func TestShellCancellationTerminatesProcessGroup(t *testing.T) {
	registry, err := tools.New(tools.Config{Allowed: []string{tools.NameShell}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = registry.Execute(ctx, runtimeapi.ToolCall{
		ID:   "shell-cancel",
		Name: tools.NameShell,
		Arguments: json.RawMessage(`{
			"command":"sleep 30",
			"working_directory":"/tmp"
		}`),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("shell cancellation was too slow: %s", time.Since(started))
	}
}
