package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/tools"
)

func TestRegistryExposesOnlyAllowedTools(t *testing.T) {
	registry, err := tools.New(tools.Config{
		Allowed: []string{tools.NameReadKnowledge, tools.NameReadKnowledge},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 1 || definitions[0].Name != tools.NameReadKnowledge {
		t.Fatalf("unexpected definitions %#v", definitions)
	}
}

func TestRegistryRejectsUnknownConfiguredTool(t *testing.T) {
	_, err := tools.New(tools.Config{Allowed: []string{"not-built-in"}})
	var toolError *tools.Error
	if !errors.As(err, &toolError) || toolError.Code != "unknown_tool" {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestRegistryReturnsDeniedAndUnknownCallsToModel(t *testing.T) {
	registry, err := tools.New(tools.Config{Allowed: []string{tools.NameReadKnowledge}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	tests := []struct {
		name string
		call runtimeapi.ToolCall
		code string
	}{
		{
			name: "known but denied",
			call: runtimeapi.ToolCall{
				ID:        "call-1",
				Name:      tools.NameShell,
				Arguments: json.RawMessage(`{}`),
			},
			code: "tool_denied",
		},
		{
			name: "unknown",
			call: runtimeapi.ToolCall{
				ID:        "call-2",
				Name:      "invented",
				Arguments: json.RawMessage(`{}`),
			},
			code: "unknown_tool",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), test.call)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !result.IsError || result.ErrorCode != test.code {
				t.Fatalf("unexpected result %#v", result)
			}
			if result.CallID != test.call.ID || result.Name != test.call.Name {
				t.Fatalf("call identity changed: %#v", result)
			}
		})
	}
}

func TestRegistryCombinesAndAllowlistsMCPTools(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	server.AddTool(
		&mcp.Tool{Name: "remote_echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "remote"}}}, nil
		},
	)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	registry, err := tools.NewWithContext(context.Background(), tools.Config{
		Allowed: []string{"remote_echo"},
		MCP: config.MCPConfig{Servers: []config.MCPServer{{
			Name: "remote", Transport: "http", Endpoint: httpServer.URL,
		}}},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	defer registry.Close(context.Background())
	definitions := registry.Definitions()
	if len(definitions) != 1 || definitions[0].Name != "remote_echo" {
		t.Fatalf("unexpected allowlisted definitions: %#v", definitions)
	}
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID: "call-remote", Name: "remote_echo", Arguments: json.RawMessage(`{}`),
	})
	if err != nil || result.Content != "remote" {
		t.Fatalf("execute MCP tool: result=%#v err=%v", result, err)
	}
	denied, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID: "call-built-in", Name: tools.NameShell, Arguments: json.RawMessage(`{}`),
	})
	if err != nil || !denied.IsError || denied.ErrorCode != "tool_denied" {
		t.Fatalf("built-in allowlist changed: result=%#v err=%v", denied, err)
	}
}

func TestRegistryRejectsMCPBuiltInNameCollision(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	server.AddTool(
		&mcp.Tool{Name: tools.NameReadKnowledge, InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	_, err := tools.NewWithContext(context.Background(), tools.Config{
		MCP: config.MCPConfig{Servers: []config.MCPServer{{
			Name: "remote", Transport: "http", Endpoint: httpServer.URL,
		}}},
	})
	var toolError *tools.Error
	if !errors.As(err, &toolError) || toolError.Code != "tool_name_collision" {
		t.Fatalf("unexpected collision error: %v", err)
	}
}

func TestRegistryStartupFailureUsesBoundedMCPCleanup(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	server.AddTool(
		&mcp.Tool{Name: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
	})
	deleteStarted := make(chan struct{}, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			deleteStarted <- struct{}{}
			<-request.Context().Done()
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()

	startedAt := time.Now()
	_, err := tools.NewWithContext(context.Background(), tools.Config{
		Allowed: []string{"unknown"},
		MCP: config.MCPConfig{Servers: []config.MCPServer{{
			Name: "remote", Transport: "http", Endpoint: httpServer.URL,
		}}},
	})
	if err == nil {
		t.Fatal("expected unknown tool startup error")
	}
	if elapsed := time.Since(startedAt); elapsed > 2500*time.Millisecond {
		t.Fatalf("startup cleanup was not bounded: %s", elapsed)
	}
	select {
	case <-deleteStarted:
	default:
		t.Fatal("startup cleanup did not attempt HTTP session close")
	}
}
