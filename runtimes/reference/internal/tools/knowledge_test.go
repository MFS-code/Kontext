package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/tools"
)

func TestReadKnowledgeReadsAndBoundsMountedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "docs", "contract.txt"),
		[]byte("reference contract"),
		0o600,
	); err != nil {
		t.Fatalf("write knowledge: %v", err)
	}
	registry, err := tools.New(tools.Config{
		Allowed:          []string{tools.NameReadKnowledge},
		KnowledgeRoot:    root,
		MaxCapturedBytes: 9,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "read-1",
		Name:      tools.NameReadKnowledge,
		Arguments: json.RawMessage(`{"path":"docs/contract.txt"}`),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Content != "reference" || !result.Truncated || result.IsError {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestReadKnowledgeTruncatesAtUTF8Boundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "unicode.txt"),
		[]byte("aéb"),
		0o600,
	); err != nil {
		t.Fatalf("write knowledge: %v", err)
	}
	registry, err := tools.New(tools.Config{
		Allowed:          []string{tools.NameReadKnowledge},
		KnowledgeRoot:    root,
		MaxCapturedBytes: 2,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "read-unicode",
		Name:      tools.NameReadKnowledge,
		Arguments: json.RawMessage(`{"path":"unicode.txt"}`),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError || result.Content != "a" || !result.Truncated {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestReadKnowledgeRejectsTraversalAndEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	registry, err := tools.New(tools.Config{
		Allowed:       []string{tools.NameReadKnowledge},
		KnowledgeRoot: root,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	for name, path := range map[string]string{
		"parent traversal": "../outside.txt",
		"absolute path":    outside,
		"escaping symlink": "link.txt",
	} {
		t.Run(name, func(t *testing.T) {
			arguments, _ := json.Marshal(map[string]string{"path": path})
			result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
				ID:        "read-denied",
				Name:      tools.NameReadKnowledge,
				Arguments: arguments,
			})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !result.IsError || result.ErrorCode != "knowledge_path_denied" {
				t.Fatalf("unexpected result %#v", result)
			}
		})
	}
}

func TestReadKnowledgeRejectsUnknownArguments(t *testing.T) {
	registry, err := tools.New(tools.Config{
		Allowed:       []string{tools.NameReadKnowledge},
		KnowledgeRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "read-invalid",
		Name:      tools.NameReadKnowledge,
		Arguments: json.RawMessage(`{"path":"file","extra":true}`),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || result.ErrorCode != "invalid_tool_arguments" {
		t.Fatalf("unexpected result %#v", result)
	}
}
