package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
)

func TestHTTPDiscoveryExecutionAuthBoundingAndClose(t *testing.T) {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "fake", Version: "1"},
		&mcp.ServerOptions{PageSize: 1},
	)
	arguments := make(chan string, 1)
	server.AddTool(
		&mcp.Tool{
			Name:        "echo",
			Description: "",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","minLength":1}}}`),
		},
		func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments <- string(request.Params.Arguments)
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "first"},
					&mcp.TextContent{Text: "second"},
				},
			}, nil
		},
	)
	server.AddTool(
		&mcp.Tool{Name: "structured", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				StructuredContent: map[string]any{"status": "ok", "count": 2},
				Content: []mcp.Content{
					&mcp.ImageContent{MIMEType: "image/png", Data: []byte("fallback")},
				},
			}, nil
		},
	)
	server.AddTool(
		&mcp.Tool{Name: "image", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: []byte("png")}},
			}, nil
		},
	)
	server.AddTool(
		&mcp.Tool{Name: "large", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", (8<<20)+1024)}},
			}, nil
		},
	)
	server.AddTool(
		&mcp.Tool{Name: "tool_error", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "recoverable"}},
				IsError: true,
			}, nil
		},
	)

	var deletes atomic.Int32
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
	})
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodDelete {
			deletes.Add(1)
		}
		handler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()

	manager, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{{
			Name:      "remote",
			Transport: "http",
			Endpoint:  httpServer.URL,
			Headers:   map[string]string{"Authorization": "Bearer test-secret"},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	definitions := manager.Definitions()
	if len(definitions) != 5 {
		t.Fatalf("unexpected definitions: %#v", definitions)
	}
	for _, definition := range definitions {
		if definition.Description == "" {
			t.Fatalf("missing fallback description for %q", definition.Name)
		}
		if len(definition.InputSchema) == 0 || definition.InputSchema[0] != '{' {
			t.Fatalf("schema is not an object for %q: %s", definition.Name, definition.InputSchema)
		}
	}

	echo, err := manager.Execute(context.Background(), "echo", json.RawMessage(`{"value":"unchanged"}`))
	if err != nil || echo.Content != "first\nsecond" {
		t.Fatalf("execute echo: result=%#v err=%v", echo, err)
	}
	if got := <-arguments; got != `{"value":"unchanged"}` {
		t.Fatalf("raw arguments changed: %q", got)
	}

	structured, err := manager.Execute(context.Background(), "structured", json.RawMessage(`{}`))
	if err != nil || !json.Valid([]byte(structured.Content)) ||
		!strings.Contains(structured.Content, `"status":"ok"`) {
		t.Fatalf("structured content was not normalized: result=%#v err=%v", structured, err)
	}

	unsupported, err := manager.Execute(context.Background(), "image", json.RawMessage(`{}`))
	if err != nil || !unsupported.IsError || unsupported.ErrorCode != "mcp_unsupported_content" {
		t.Fatalf("unsupported content was not rejected: result=%#v err=%v", unsupported, err)
	}

	large, err := manager.Execute(context.Background(), "large", json.RawMessage(`{}`))
	if err != nil || !large.Truncated || len(large.Content) > 8<<20 {
		t.Fatalf("large output was not bounded: bytes=%d result=%#v err=%v", len(large.Content), large, err)
	}

	toolError, err := manager.Execute(context.Background(), "tool_error", json.RawMessage(`{}`))
	if err != nil || !toolError.IsError || toolError.ErrorCode != "mcp_tool_error" ||
		toolError.Content != "recoverable" {
		t.Fatalf("MCP IsError was not preserved: result=%#v err=%v", toolError, err)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if deletes.Load() == 0 {
		t.Fatal("HTTP MCP session was not closed")
	}
}

func TestBoundedHTTPTransportPreservesDefaultSSEStreaming(t *testing.T) {
	notifications := make(chan string, 1)
	server := mcp.NewServer(&mcp.Implementation{Name: "sse", Version: "1"}, nil)
	server.AddTool(
		&mcp.Tool{Name: "notify", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if err := request.Session.NotifyProgress(
				context.Background(),
				&mcp.ProgressNotificationParams{Message: "streamed"},
			); err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "done"}},
			}, nil
		},
	)
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		nil,
	))
	defer httpServer.Close()

	client := mcp.NewClient(
		&mcp.Implementation{Name: "sse-client", Version: "1"},
		&mcp.ClientOptions{
			Capabilities: &mcp.ClientCapabilities{},
			ProgressNotificationHandler: func(
				_ context.Context,
				request *mcp.ProgressNotificationClientRequest,
			) {
				notifications <- request.Params.Message
			},
		},
	)
	httpClient := &http.Client{Transport: &boundedHTTPTransport{
		base:           http.DefaultTransport,
		endpointOrigin: origin(mustParseURL(t, httpServer.URL)),
		maxWireBytes:   maxHTTPWireBytes,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   httpServer.URL,
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect default SSE client: %v", err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "notify",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil || len(result.Content) != 1 {
		t.Fatalf("call through default SSE transport: result=%#v err=%v", result, err)
	}
	select {
	case message := <-notifications:
		if message != "streamed" {
			t.Fatalf("unexpected progress notification %q", message)
		}
	case <-ctx.Done():
		t.Fatal("default SSE server-to-client notification did not arrive")
	}
}

func TestNormalizeNilSchemaUsesObjectFallback(t *testing.T) {
	schema, _, err := normalizeSchema(nil)
	if err != nil || string(schema) != `{"type":"object"}` {
		t.Fatalf("unexpected fallback schema %s: %v", schema, err)
	}
}

func TestHTTPCallCancellationAndReconnectAfterProtocolError(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	started := make(chan struct{})
	server.AddTool(
		&mcp.Tool{Name: "cancel", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	var failures atomic.Int32
	server.AddTool(
		&mcp.Tool{Name: "flaky", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if failures.Add(1) == 1 {
				return nil, errors.New("protocol failure")
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "reconnected"}}}, nil
		},
	)

	var initializations atomic.Int32
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
	})
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.Header.Get("Mcp-Session-Id") == "" {
			initializations.Add(1)
		}
		handler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()

	manager, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{{Name: "remote", Transport: "http", Endpoint: httpServer.URL}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer manager.Close(context.Background())

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, callErr := manager.Execute(cancelCtx, "cancel", json.RawMessage(`{}`))
		cancelled <- callErr
	}()
	<-started
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}

	first, err := manager.Execute(context.Background(), "flaky", json.RawMessage(`{}`))
	if err != nil || !first.IsError {
		t.Fatalf("expected protocol tool error, result=%#v err=%v", first, err)
	}
	second, err := manager.Execute(context.Background(), "flaky", json.RawMessage(`{}`))
	if err != nil || second.Content != "reconnected" {
		t.Fatalf("expected reconnect success, result=%#v err=%v", second, err)
	}
	if initializations.Load() < 2 {
		t.Fatalf("expected a new MCP session, got %d initializations", initializations.Load())
	}
}

func TestNormalizeSchemaRejectsMalformedSchema(t *testing.T) {
	_, _, err := normalizeSchema(json.RawMessage(`{"type":"object","properties":"not-an-object"}`))
	if err == nil || !strings.Contains(err.Error(), "input schema is invalid") {
		t.Fatalf("expected malformed schema error, got %v", err)
	}
}

func TestReconnectRejectsChangedFrozenDefinitions(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(
		&mcp.Tool{
			Name:        "mutable",
			Description: "original",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if calls.Add(1) == 1 {
				server.AddTool(
					&mcp.Tool{
						Name:        "mutable",
						Description: "changed",
						InputSchema: json.RawMessage(`{"type":"object","properties":{"new":{"type":"string"}}}`),
					},
					func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						return &mcp.CallToolResult{
							Content: []mcp.Content{&mcp.TextContent{Text: "must not run"}},
						}, nil
					},
				)
				return nil, errors.New("force reconnect")
			}
			return &mcp.CallToolResult{}, nil
		},
	)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	manager, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{{Name: "remote", Transport: "http", Endpoint: httpServer.URL}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer manager.Close(context.Background())
	first, err := manager.Execute(context.Background(), "mutable", json.RawMessage(`{}`))
	if err != nil || !first.IsError {
		t.Fatalf("expected initial protocol failure: result=%#v err=%v", first, err)
	}
	second, err := manager.Execute(context.Background(), "mutable", json.RawMessage(`{}`))
	if err != nil || !second.IsError || second.ErrorCode != "mcp_reconnect_failed" ||
		!strings.Contains(second.Content, "definitions changed") {
		t.Fatalf("changed definitions were not rejected: result=%#v err=%v", second, err)
	}
}

func TestHTTPCrossOriginRedirectDoesNotForwardAuthorization(t *testing.T) {
	var redirectedRequests atomic.Int32
	var redirectedAuthorization atomic.Value
	redirected := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		redirectedRequests.Add(1)
		redirectedAuthorization.Store(request.Header.Get("Authorization"))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer redirected.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(writer, request, redirected.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	_, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{{
			Name:      "redirector",
			Transport: "http",
			Endpoint:  redirector.URL,
			Headers:   map[string]string{"Authorization": "Bearer must-not-leak"},
		}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "cross-origin redirect") {
		t.Fatalf("expected cross-origin redirect rejection, got %v", err)
	}
	if redirectedRequests.Load() != 0 {
		value, _ := redirectedAuthorization.Load().(string)
		t.Fatalf("redirected host received %d requests with Authorization %q", redirectedRequests.Load(), value)
	}
}

func TestHTTPOriginNormalizesDefaultPorts(t *testing.T) {
	tests := []struct {
		left  string
		right string
	}{
		{left: "https://example.com/mcp", right: "https://example.com:443/other"},
		{left: "http://example.com/mcp", right: "http://example.com:80/other"},
	}
	for _, test := range tests {
		left := mustParseURL(t, test.left)
		right := mustParseURL(t, test.right)
		if origin(left) != origin(right) {
			t.Fatalf("default port changed origin: %q != %q", origin(left), origin(right))
		}
		if err := sameOriginRedirectPolicy(left)(
			&http.Request{URL: right},
			[]*http.Request{{URL: left}},
		); err != nil {
			t.Fatalf("same-origin default-port redirect was rejected: %v", err)
		}
	}
}

func TestHTTPTransportInjectsHeadersOnlyForConfiguredOrigin(t *testing.T) {
	var observed string
	transport := &boundedHTTPTransport{
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			observed = request.Header.Get("Authorization")
			return &http.Response{
				StatusCode:    http.StatusNoContent,
				ContentLength: 0,
				Body:          http.NoBody,
				Header:        make(http.Header),
			}, nil
		}),
		endpointOrigin: "https://configured.example",
		headers:        map[string]string{"Authorization": "Bearer secret"},
		maxWireBytes:   32,
	}
	request, err := http.NewRequest(http.MethodPost, "https://other.example/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer inherited")
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if observed != "" {
		t.Fatalf("Authorization reached non-configured origin: %q", observed)
	}
}

func TestHTTPRejectsOversizedProtocolResponseBeforeSDKDecode(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", fmt.Sprintf("%d", maxHTTPWireBytes+1))
		writer.WriteHeader(http.StatusOK)
	}))
	defer httpServer.Close()
	_, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{{Name: "large", Transport: "http", Endpoint: httpServer.URL}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "wire limit") {
		t.Fatalf("expected wire-limit error, got %v", err)
	}
}

func TestHTTPRejectsChunkedProtocolResponseOverWireLimit(t *testing.T) {
	transport := &boundedHTTPTransport{
		base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: -1,
				Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", 33))),
				Header:        make(http.Header),
			}, nil
		}),
		endpointOrigin: "https://example.test",
		maxWireBytes:   32,
	}
	request, err := http.NewRequest(http.MethodPost, "https://example.test/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("round trip before streaming read: %v", err)
	}
	_, err = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !errors.Is(err, errMCPHTTPWireLimit) {
		t.Fatalf("expected stable chunked wire-limit error, got %v", err)
	}
}

func TestHTTPDeleteDeadlineLivesUntilBodyClose(t *testing.T) {
	var requestContext context.Context
	transport := &boundedHTTPTransport{
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			requestContext = request.Context()
			return &http.Response{
				StatusCode:    http.StatusNoContent,
				ContentLength: 0,
				Body:          io.NopCloser(strings.NewReader("")),
				Header:        make(http.Header),
			}, nil
		}),
		endpointOrigin: "https://example.test",
		maxWireBytes:   32,
	}
	request, err := http.NewRequest(http.MethodDelete, "https://example.test/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	select {
	case <-requestContext.Done():
		t.Fatal("DELETE context was cancelled before response body close")
	default:
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	select {
	case <-requestContext.Done():
	case <-time.After(time.Second):
		t.Fatal("DELETE context stayed alive after response body close")
	}
}

func TestMCPExternalErrorsAreRedactedAndBounded(t *testing.T) {
	const secret = "resolved-super-secret"
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	server.AddTool(
		&mcp.Tool{Name: "fail", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, errors.New(secret + strings.Repeat("x", int(maxExternalErrorBytes*2)))
		},
	)
	server.AddTool(
		&mcp.Tool{
			Name:        "echo_secret",
			Description: "uses " + secret,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "value=" + secret}},
			}, nil
		},
	)
	manager, closeServer := newHTTPTestManager(t, server, config.MCPServer{
		Name:            "redacted",
		Transport:       "http",
		SensitiveValues: []string{secret},
	})
	defer closeServer()
	for _, definition := range manager.Definitions() {
		if strings.Contains(definition.Description, secret) {
			t.Fatalf("resolved value leaked through tool description: %#v", definition)
		}
	}
	result, err := manager.Execute(context.Background(), "fail", json.RawMessage(`{}`))
	if err != nil || !result.IsError {
		t.Fatalf("expected bounded tool error: result=%#v err=%v", result, err)
	}
	if strings.Contains(result.Content, secret) ||
		!strings.Contains(result.Content, "[REDACTED]") ||
		len(result.Content) > int(maxExternalErrorBytes) {
		t.Fatalf("external error was not safely normalized: bytes=%d content=%q", len(result.Content), result.Content)
	}
	success, err := manager.Execute(context.Background(), "echo_secret", json.RawMessage(`{}`))
	if err != nil || strings.Contains(success.Content, secret) ||
		success.Content != "value=[REDACTED]" {
		t.Fatalf("resolved value leaked through successful MCP content: result=%#v err=%v", success, err)
	}
}

func TestMCPStartupErrorsDoNotExposeResolvedValues(t *testing.T) {
	const secret = "startup-resolved-secret"
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, secret+" is not an MCP response")
	}))
	defer httpServer.Close()
	_, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{{
			Name:            "invalid",
			Transport:       "http",
			Endpoint:        httpServer.URL,
			SensitiveValues: []string{secret},
		}},
	}, nil)
	if err == nil {
		t.Fatal("expected startup failure")
	}
	if strings.Contains(err.Error(), secret) || len(err.Error()) > int(maxExternalErrorBytes)+64 {
		t.Fatalf("startup error exposed or failed to bound resolved value: %q", err)
	}
}

func TestResolvedValuesAreRedactedAcrossStderrWrites(t *testing.T) {
	var output bytes.Buffer
	writer := newRedactingLineWriter(&output, newRedactor([]string{"split-secret"}))
	for _, part := range []string{"server saw split", "-secret in environment\n"} {
		if _, err := writer.Write([]byte(part)); err != nil {
			t.Fatalf("write stderr: %v", err)
		}
	}
	if strings.Contains(output.String(), "split-secret") ||
		!strings.Contains(output.String(), "[REDACTED]") {
		t.Fatalf("resolved value leaked through stderr: %q", output.String())
	}
}

func TestStructuredContentRedactionPreservesJSONSyntax(t *testing.T) {
	secrets := []string{"quote\"\nsecret", `":`, "[]"}
	content, resultErr := normalizeResult(&mcp.CallToolResult{
		StructuredContent: map[string]any{
			"quoted": "prefix quote\"\nsecret suffix",
			"short":  "[]",
			"syntax": `before ": after`,
			"nested": []any{
				map[string]any{"value": "quote\"\nsecret"},
			},
			"[]": "key is not a value",
		},
	}, newRedactor(secrets))
	if resultErr != nil {
		t.Fatalf("normalize structured content: %v", resultErr)
	}
	if !json.Valid([]byte(content)) {
		t.Fatalf("redaction corrupted JSON syntax: %q", content)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("decode redacted JSON: %v", err)
	}
	if decoded["short"] != "[REDACTED]" ||
		decoded["quoted"] != "prefix [REDACTED] suffix" ||
		decoded["[]"] != "key is not a value" {
		t.Fatalf("unexpected value-level redaction: %#v", decoded)
	}
	var assertRedacted func(any)
	assertRedacted = func(value any) {
		switch typed := value.(type) {
		case string:
			for _, secret := range secrets {
				if strings.Contains(typed, secret) {
					t.Fatalf("structured value leaked secret %q: %q", secret, typed)
				}
			}
		case []any:
			for _, child := range typed {
				assertRedacted(child)
			}
		case map[string]any:
			for _, child := range typed {
				assertRedacted(child)
			}
		}
	}
	assertRedacted(decoded)
}

func TestDiscoveryLimitsAndToolNames(t *testing.T) {
	tests := []struct {
		name  string
		build func(*mcp.Server)
		code  string
	}{
		{
			name: "tool count",
			build: func(server *mcp.Server) {
				for index := 0; index <= maxDiscoveredTools; index++ {
					addStaticTool(server, fmt.Sprintf("tool_%03d", index), "", json.RawMessage(`{"type":"object"}`))
				}
			},
			code: "mcp_discovery_limit_exceeded",
		},
		{
			name: "description bytes",
			build: func(server *mcp.Server) {
				addStaticTool(
					server,
					"large_description",
					strings.Repeat("d", maxToolDescriptionBytes+1),
					json.RawMessage(`{"type":"object"}`),
				)
			},
			code: "mcp_discovery_limit_exceeded",
		},
		{
			name: "schema bytes",
			build: func(server *mcp.Server) {
				schema, _ := json.Marshal(map[string]any{
					"type":        "object",
					"x-oversized": strings.Repeat("s", maxToolSchemaBytes),
				})
				addStaticTool(server, "large_schema", "", schema)
			},
			code: "mcp_discovery_limit_exceeded",
		},
		{
			name: "total definition bytes",
			build: func(server *mcp.Server) {
				for index := 0; index < maxDiscoveredTools; index++ {
					addStaticTool(
						server,
						fmt.Sprintf("total_%03d", index),
						strings.Repeat("t", maxToolDescriptionBytes),
						json.RawMessage(`{"type":"object"}`),
					)
				}
			},
			code: "mcp_discovery_limit_exceeded",
		},
		{
			name: "invalid tool name",
			build: func(server *mcp.Server) {
				addStaticTool(server, "invalid tool", "", json.RawMessage(`{"type":"object"}`))
			},
			code: "mcp_invalid_tool_definition",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := mcp.NewServer(
				&mcp.Implementation{Name: "limits", Version: "1"},
				&mcp.ServerOptions{PageSize: 25},
			)
			test.build(server)
			handler := mcp.NewStreamableHTTPHandler(
				func(*http.Request) *mcp.Server { return server },
				&mcp.StreamableHTTPOptions{JSONResponse: true},
			)
			httpServer := httptest.NewServer(handler)
			defer httpServer.Close()
			_, err := New(context.Background(), config.MCPConfig{
				Servers: []config.MCPServer{{Name: "limits", Transport: "http", Endpoint: httpServer.URL}},
			}, nil)
			var clientError *Error
			if !errors.As(err, &clientError) || clientError.Code != test.code {
				t.Fatalf("expected %s, got %v", test.code, err)
			}
		})
	}
}

func TestFrozenSchemaValidatesArgumentsWithoutStalingSession(t *testing.T) {
	var calls atomic.Int32
	var initializations atomic.Int32
	server := mcp.NewServer(
		&mcp.Implementation{Name: "arguments", Version: "1"},
		&mcp.ServerOptions{
			InitializedHandler: func(context.Context, *mcp.InitializedRequest) {
				initializations.Add(1)
			},
		},
	)
	server.AddTool(
		&mcp.Tool{
			Name: "validated",
			InputSchema: json.RawMessage(
				`{"type":"object","properties":{"value":{"type":"string","minLength":2}},"required":["value"],"additionalProperties":false}`,
			),
		},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "called"}}}, nil
		},
	)
	manager, closeServer := newHTTPTestManager(t, server, config.MCPServer{
		Name:      "arguments",
		Transport: "http",
	})
	defer closeServer()
	for _, arguments := range []json.RawMessage{
		json.RawMessage(`[]`),
		json.RawMessage(`{"value":"x"}`),
		json.RawMessage(`{"value":"ok","extra":true}`),
	} {
		result, err := manager.Execute(context.Background(), "validated", arguments)
		if err != nil || !result.IsError || result.ErrorCode != "mcp_invalid_arguments" {
			t.Fatalf("expected local argument rejection: result=%#v err=%v", result, err)
		}
	}
	valid, err := manager.Execute(context.Background(), "validated", json.RawMessage(`{"value":"ok"}`))
	if err != nil || valid.Content != "called" {
		t.Fatalf("valid call failed: result=%#v err=%v", valid, err)
	}
	if calls.Load() != 1 || initializations.Load() != 1 {
		t.Fatalf("invalid arguments reached server or staled session: calls=%d initializations=%d", calls.Load(), initializations.Load())
	}
}

func TestCloseUsesConcurrentBoundedHTTPDeletes(t *testing.T) {
	stalledServer := mcp.NewServer(&mcp.Implementation{Name: "stalled", Version: "1"}, nil)
	addStaticTool(stalledServer, "stalled_tool", "", json.RawMessage(`{"type":"object"}`))
	stalledHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return stalledServer },
		&mcp.StreamableHTTPOptions{JSONResponse: true},
	)
	deleteStarted := make(chan struct{}, 1)
	stalledHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			deleteStarted <- struct{}{}
			<-request.Context().Done()
			return
		}
		stalledHandler.ServeHTTP(writer, request)
	}))
	defer stalledHTTP.Close()

	healthyServer := mcp.NewServer(&mcp.Implementation{Name: "healthy", Version: "1"}, nil)
	addStaticTool(healthyServer, "healthy_tool", "", json.RawMessage(`{"type":"object"}`))
	healthyHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return healthyServer },
		&mcp.StreamableHTTPOptions{JSONResponse: true},
	)
	healthyDeleted := make(chan struct{}, 1)
	healthyHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			healthyDeleted <- struct{}{}
		}
		healthyHandler.ServeHTTP(writer, request)
	}))
	defer healthyHTTP.Close()

	manager, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{
			{Name: "stalled", Transport: "http", Endpoint: stalledHTTP.URL},
			{Name: "healthy", Transport: "http", Endpoint: healthyHTTP.URL},
		},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	startedAt := time.Now()
	if err := manager.Close(closeCtx); err == nil {
		t.Fatal("expected stalled DELETE cleanup error")
	}
	if elapsed := time.Since(startedAt); elapsed > 2500*time.Millisecond {
		t.Fatalf("close exceeded bounded DELETE deadline: %s", elapsed)
	}
	select {
	case <-deleteStarted:
	default:
		t.Fatal("stalled DELETE did not start")
	}
	select {
	case <-healthyDeleted:
	default:
		t.Fatal("stalled server starved healthy server cleanup")
	}
}

func addStaticTool(
	server *mcp.Server,
	name string,
	description string,
	schema json.RawMessage,
) {
	server.AddTool(
		&mcp.Tool{Name: name, Description: description, InputSchema: schema},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	)
}

func newHTTPTestManager(
	t *testing.T,
	server *mcp.Server,
	serverConfig config.MCPServer,
) (*Manager, func()) {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{JSONResponse: true},
	)
	httpServer := httptest.NewServer(handler)
	serverConfig.Endpoint = httpServer.URL
	manager, err := New(context.Background(), config.MCPConfig{
		Servers: []config.MCPServer{serverConfig},
	}, nil)
	if err != nil {
		httpServer.Close()
		t.Fatalf("connect: %v", err)
	}
	return manager, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Close(ctx)
		httpServer.Close()
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}
