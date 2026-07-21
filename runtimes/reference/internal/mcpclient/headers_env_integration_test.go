package mcpclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/mcpclient"
)

func TestHeadersFromEnvAuthenticatesHTTPMCPWithoutLeaking(t *testing.T) {
	const (
		envName = "MCP_AUTH_HEADER"
		secret  = "Bearer runtime-integration-secret"
	)
	server := mcp.NewServer(&mcp.Implementation{Name: "authenticated", Version: "1"}, nil)
	server.AddTool(
		&mcp.Tool{Name: "fail_safely", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, errors.New("authenticated upstream returned " + secret)
		},
	)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{JSONResponse: true},
	)
	var authenticatedRequests atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != secret {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		authenticatedRequests.Add(1)
		handler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()

	mcpDocument, err := json.Marshal(map[string]any{
		"servers": []map[string]any{{
			"name":           "authenticated",
			"transport":      "http",
			"endpoint":       httpServer.URL,
			"headersFromEnv": map[string]string{"Authorization": envName},
		}},
	})
	if err != nil {
		t.Fatalf("marshal MCP configuration: %v", err)
	}
	values := map[string]string{
		"KONTEXT_GOAL":       "call an authenticated MCP server",
		"KONTEXT_PROVIDER":   "fake",
		"KONTEXT_MODEL":      "test/model",
		"KONTEXT_MCP_CONFIG": string(mcpDocument),
		envName:              secret,
	}
	runtimeConfig, err := config.Load(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal("load runtime config failed")
	}
	if len(runtimeConfig.MCP.Servers) != 1 ||
		runtimeConfig.MCP.Servers[0].Headers["Authorization"] != secret {
		t.Fatalf("headersFromEnv did not resolve runtime env name %q", envName)
	}

	var stderr bytes.Buffer
	manager, err := mcpclient.New(context.Background(), runtimeConfig.MCP, &stderr)
	if err != nil {
		t.Fatal("connect authenticated MCP server failed")
	}
	result, callErr := manager.Execute(
		context.Background(),
		"fail_safely",
		json.RawMessage(`{}`),
	)
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	closeErr := manager.Close(closeCtx)
	if callErr != nil || closeErr != nil || !result.IsError {
		t.Fatal("authenticated MCP call did not return a safe tool error")
	}
	if authenticatedRequests.Load() < 2 {
		t.Fatalf("expected authenticated initialize and tool requests, got %d", authenticatedRequests.Load())
	}
	observable := strings.Join([]string{
		result.Content,
		stderr.String(),
		fmt.Sprint(callErr),
		fmt.Sprint(closeErr),
	}, "\n")
	if strings.Contains(observable, secret) {
		t.Fatal("MCP error or logs exposed the resolved Secret")
	}
	if !strings.Contains(result.Content, "[REDACTED]") {
		t.Fatalf("MCP error did not carry a redaction marker: %q", result.Content)
	}
}
