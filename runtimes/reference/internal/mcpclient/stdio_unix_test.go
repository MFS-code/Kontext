//go:build linux || darwin

package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
)

func TestStdioDiscoveryCallAndProcessGroupCleanup(t *testing.T) {
	manager, grandchildPID := startStdioTestManager(t, "")
	result, err := manager.Execute(context.Background(), "echo", json.RawMessage(`{"value":"stdio"}`))
	if err != nil || result.Content != `{"value":"stdio"}` {
		t.Fatalf("stdio call failed: result=%#v err=%v", result, err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatalf("close stdio manager: %v", err)
	}
	if err := manager.Close(closeCtx); err != nil {
		t.Fatalf("idempotent stdio close: %v", err)
	}
	waitForProcessExit(t, grandchildPID)
}

func TestStdioCancellationCleansGrandchild(t *testing.T) {
	callMarker := t.TempDir() + "/call-started"
	manager, grandchildPID := startStdioTestManager(t, callMarker)
	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		_, err := manager.Execute(ctx, "wait", json.RawMessage(`{}`))
		callDone <- err
	}()
	waitForFile(t, callMarker)
	cancel()
	if err := <-callDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled call, got %v", err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeCancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatalf("close cancelled stdio manager: %v", err)
	}
	waitForProcessExit(t, grandchildPID)
}

func TestStdioRejectsOversizedFrameAndReapsProcess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	pidFile := t.TempDir() + "/oversized.pid"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = New(ctx, config.MCPConfig{
		Servers: []config.MCPServer{{
			Name:      "oversized",
			Transport: "stdio",
			Command:   executable,
			Args:      []string{"-test.run=^TestMCPStdioHelperProcess$"},
			Env: map[string]string{
				"KONTEXT_MCP_TEST_HELPER":     "1",
				"KONTEXT_MCP_OVERSIZED_FRAME": "1",
				"KONTEXT_MCP_HELPER_PID_FILE": pidFile,
			},
		}},
	}, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), errMCPStdioFrameLimit.Error()) {
		t.Fatalf("expected stable stdio frame-limit error, got %v", err)
	}
	waitForFile(t, pidFile)
	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read helper pid: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		t.Fatalf("parse helper pid: %v", parseErr)
	}
	waitForProcessExit(t, pid)
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("KONTEXT_MCP_TEST_HELPER") != "1" {
		return
	}
	if os.Getenv("KONTEXT_MCP_OVERSIZED_FRAME") == "1" {
		if err := os.WriteFile(
			os.Getenv("KONTEXT_MCP_HELPER_PID_FILE"),
			[]byte(strconv.Itoa(os.Getpid())),
			0o600,
		); err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"padding":"`)
		chunk := []byte(strings.Repeat("x", 1<<20))
		for written := int64(0); written <= maxStdioFrameBytes; written += int64(len(chunk)) {
			if _, err := os.Stdout.Write(chunk); err != nil {
				os.Exit(0)
			}
		}
		os.Exit(0)
	}
	grandchild := exec.Command("/bin/sh", "-c", `trap '' TERM; while :; do /bin/sleep 10; done`)
	if err := grandchild.Start(); err != nil {
		t.Fatalf("start grandchild: %v", err)
	}
	if err := os.WriteFile(
		os.Getenv("KONTEXT_MCP_GRANDCHILD_PID_FILE"),
		[]byte(strconv.Itoa(grandchild.Process.Pid)),
		0o600,
	); err != nil {
		t.Fatalf("write grandchild pid: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "stdio-helper", Version: "1"}, nil)
	server.AddTool(
		&mcp.Tool{
			Name:        "echo",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
		},
		func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				StructuredContent: json.RawMessage(request.Params.Arguments),
			}, nil
		},
	)
	server.AddTool(
		&mcp.Tool{Name: "wait", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if marker := os.Getenv("KONTEXT_MCP_CALL_MARKER"); marker != "" {
				if err := os.WriteFile(marker, []byte("started"), 0o600); err != nil {
					return nil, err
				}
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatalf("run stdio MCP server: %v", err)
	}
}

func startStdioTestManager(t *testing.T, callMarker string) (*Manager, int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	pidFile := t.TempDir() + "/grandchild.pid"
	manager, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{{
			Name:      "stdio",
			Transport: "stdio",
			Command:   executable,
			Args:      []string{"-test.run=^TestMCPStdioHelperProcess$"},
			Env: map[string]string{
				"KONTEXT_MCP_TEST_HELPER":         "1",
				"KONTEXT_MCP_GRANDCHILD_PID_FILE": pidFile,
				"KONTEXT_MCP_CALL_MARKER":         callMarker,
			},
		}},
	}, os.Stderr)
	if err != nil {
		t.Fatalf("start stdio manager: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	waitForFile(t, pidFile)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse grandchild pid: %v", err)
	}
	return manager, pid
}

func waitForFile(t *testing.T, filename string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filename); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", filename)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d remained alive", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
