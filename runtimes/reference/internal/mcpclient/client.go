package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

const (
	maxCapturedBytes            = int64(8 << 20)
	maxHTTPWireBytes            = int64(64 << 20)
	maxStdioFrameBytes          = int64(64 << 20)
	maxDiscoveredTools          = 256
	maxToolDescriptionBytes     = 16 << 10
	maxToolSchemaBytes          = 256 << 10
	maxToolDefinitionBytes      = 280 << 10
	maxTotalToolDefinitionBytes = 4 << 20
	maxExternalErrorBytes       = int64(4 << 10)
	maxStderrLineBytes          = 64 << 10
	httpCloseRequestTimeout     = 2 * time.Second
	processTerminationGrace     = 500 * time.Millisecond
	processGroupPollInterval    = 10 * time.Millisecond
	fallbackDescriptionPrefix   = "MCP tool"
)

var mcpToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type Error struct {
	Code    string
	Message string
}

func (err *Error) Error() string {
	if err.Code == "" {
		return err.Message
	}
	return fmt.Sprintf("%s: %s", err.Code, err.Message)
}

type Result struct {
	Content   string
	IsError   bool
	ErrorCode string
	Truncated bool
}

type Manager struct {
	servers         []*server
	tools           map[string]*remoteTool
	definitions     []runtimeapi.ToolDefinition
	definitionBytes int
}

type remoteTool struct {
	name   string
	server *server
	schema *jsonschema.Resolved
}

type frozenTool struct {
	name       string
	definition runtimeapi.ToolDefinition
	schema     *jsonschema.Resolved
}

type server struct {
	mu       sync.Mutex
	config   config.MCPServer
	stderr   io.Writer
	client   *mcp.Client
	session  *mcp.ClientSession
	command  *managedCommand
	stale    bool
	frozen   []frozenTool
	redactor redactor
}

func New(ctx context.Context, mcpConfig config.MCPConfig, stderr io.Writer) (*Manager, error) {
	if stderr == nil {
		stderr = io.Discard
	}
	manager := &Manager{
		tools: make(map[string]*remoteTool),
	}
	for _, serverConfig := range mcpConfig.Servers {
		current := &server{
			config:   serverConfig,
			stderr:   stderr,
			redactor: newRedactor(serverConfig.SensitiveValues),
		}
		if err := current.connect(ctx); err != nil {
			manager.closeAfterStartupFailure()
			var clientError *Error
			if errors.As(err, &clientError) {
				return nil, clientError
			}
			return nil, current.safeError(
				"mcp_connection_failed",
				fmt.Sprintf("connect MCP server %q", serverConfig.Name),
				err,
			)
		}
		manager.servers = append(manager.servers, current)
		for _, discovered := range current.frozen {
			if len(manager.definitions) >= maxDiscoveredTools {
				manager.closeAfterStartupFailure()
				return nil, &Error{
					Code: "mcp_discovery_limit_exceeded",
					Message: fmt.Sprintf(
						"configured MCP servers expose more than %d tools in total",
						maxDiscoveredTools,
					),
				}
			}
			if _, collision := manager.tools[discovered.name]; collision {
				manager.closeAfterStartupFailure()
				return nil, &Error{
					Code:    "mcp_invalid_tool_definition",
					Message: fmt.Sprintf("MCP tool name %q is provided more than once", discovered.name),
				}
			}
			manager.tools[discovered.name] = &remoteTool{
				name:   discovered.name,
				server: current,
				schema: discovered.schema,
			}
			manager.definitionBytes += len(discovered.definition.Name) +
				len(discovered.definition.Description) +
				len(discovered.definition.InputSchema)
			if manager.definitionBytes > maxTotalToolDefinitionBytes {
				manager.closeAfterStartupFailure()
				return nil, &Error{
					Code: "mcp_discovery_limit_exceeded",
					Message: fmt.Sprintf(
						"configured MCP tool definitions exceed the %d-byte total limit",
						maxTotalToolDefinitionBytes,
					),
				}
			}
			manager.definitions = append(manager.definitions, discovered.definition)
		}
	}
	return manager, nil
}

func (manager *Manager) Definitions() []runtimeapi.ToolDefinition {
	if manager == nil {
		return nil
	}
	return runtimeapi.CloneToolDefinitions(manager.definitions)
}

func (manager *Manager) Execute(
	ctx context.Context,
	name string,
	arguments json.RawMessage,
) (Result, error) {
	if manager == nil {
		return Result{IsError: true, ErrorCode: "mcp_unavailable", Content: "MCP execution is unavailable"}, nil
	}
	selected, exists := manager.tools[name]
	if !exists {
		return Result{IsError: true, ErrorCode: "unknown_tool", Content: fmt.Sprintf("tool %q is not available", name)}, nil
	}
	if err := validateArguments(selected.name, selected.schema, arguments); err != nil {
		return Result{
			IsError:   true,
			ErrorCode: "mcp_invalid_arguments",
			Content:   err.Error(),
		}, nil
	}
	return selected.server.call(ctx, selected.name, arguments)
}

func (manager *Manager) Close(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	errorsByServer := make([]error, len(manager.servers))
	var waitGroup sync.WaitGroup
	for index, current := range manager.servers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := current.close(ctx); err != nil {
				errorsByServer[index] = current.safeError(
					"mcp_cleanup_failed",
					fmt.Sprintf("close MCP server %q", current.config.Name),
					err,
				)
			}
		}()
	}
	waitGroup.Wait()
	return errors.Join(errorsByServer...)
}

func (manager *Manager) closeAfterStartupFailure() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = manager.Close(ctx)
}

func (current *server) connect(ctx context.Context) error {
	current.client = mcp.NewClient(
		&mcp.Implementation{Name: "kontext-reference", Version: "v1alpha1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	transport, command, err := current.transport()
	if err != nil {
		return current.safeError(
			"mcp_connection_failed",
			fmt.Sprintf("start MCP server %q", current.config.Name),
			err,
		)
	}
	current.command = command
	connectCtx, cancel := operationContext(ctx, current.config.Timeout)
	defer cancel()
	session, err := current.client.Connect(connectCtx, transport, nil)
	if err != nil {
		current.terminateCommand(connectCtx, command)
		current.command = nil
		return current.safeError(
			"mcp_connection_failed",
			fmt.Sprintf("initialize MCP server %q", current.config.Name),
			err,
		)
	}
	current.session = session

	discovered, err := discoverTools(
		connectCtx,
		current.config.Name,
		session,
		current.redactor,
	)
	if err != nil {
		_ = current.closeSession(ctx)
		var clientError *Error
		if errors.As(err, &clientError) {
			clientError.Message = current.redactor.clean(clientError.Message)
			return clientError
		}
		return current.safeError(
			"mcp_discovery_failed",
			fmt.Sprintf("discover MCP server %q tools", current.config.Name),
			err,
		)
	}
	if current.frozen != nil && !sameTools(current.frozen, discovered) {
		_ = current.closeSession(ctx)
		return &Error{
			Code:    "mcp_toolset_changed",
			Message: fmt.Sprintf("MCP server %q tool definitions changed after reconnect", current.config.Name),
		}
	}
	if current.frozen == nil {
		current.frozen = discovered
	}
	current.stale = false
	return nil
}

func (current *server) transport() (mcp.Transport, *managedCommand, error) {
	if current.config.Transport == "http" {
		retries := 0
		if current.config.MaxRetries != nil {
			retries = *current.config.MaxRetries
			if retries == 0 {
				retries = -1
			}
		}
		endpoint, err := url.Parse(current.config.Endpoint)
		if err != nil {
			panic("validated MCP endpoint could not be parsed")
		}
		httpClient := &http.Client{
			Transport: &boundedHTTPTransport{
				base:           http.DefaultTransport,
				endpointOrigin: origin(endpoint),
				headers:        cloneStrings(current.config.Headers),
				maxWireBytes:   maxHTTPWireBytes,
			},
			CheckRedirect: sameOriginRedirectPolicy(endpoint),
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   current.config.Endpoint,
			HTTPClient: httpClient,
			MaxRetries: retries,
		}, nil, nil
	}
	return startCommandTransport(
		current.config,
		current.stderr,
		current.redactor,
		maxStdioFrameBytes,
	)
}

func (current *server) call(
	ctx context.Context,
	name string,
	arguments json.RawMessage,
) (Result, error) {
	current.mu.Lock()
	defer current.mu.Unlock()

	if current.stale {
		if err := current.closeSession(ctx); err != nil {
			return Result{
				IsError:   true,
				ErrorCode: "mcp_reconnect_failed",
				Content:   fmt.Sprintf("MCP server %q could not close its stale session", current.config.Name),
			}, nil
		}
		if err := current.connect(ctx); err != nil {
			current.stale = true
			return Result{
				IsError:   true,
				ErrorCode: "mcp_reconnect_failed",
				Content: current.safeMessage(
					fmt.Sprintf("MCP server %q could not reconnect: %v", current.config.Name, err),
				),
			}, nil
		}
	}

	callCtx, cancel := operationContext(ctx, current.config.Timeout)
	defer cancel()
	response, err := current.session.CallTool(callCtx, &mcp.CallToolParams{
		Name:      name,
		Arguments: json.RawMessage(arguments),
	})
	if err != nil {
		current.stale = true
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Result{
				IsError:   true,
				ErrorCode: "mcp_timeout",
				Content:   fmt.Sprintf("MCP tool %q timed out", name),
			}, nil
		}
		code := "mcp_protocol_error"
		if errors.Is(err, mcp.ErrConnectionClosed) {
			code = "mcp_transport_error"
		}
		return Result{
			IsError:   true,
			ErrorCode: code,
			Content: current.safeMessage(
				fmt.Sprintf("MCP tool %q failed: %v", name, err),
			),
		}, nil
	}

	content, normalizeErr := normalizeResult(response, current.redactor)
	if normalizeErr != nil {
		return Result{
			IsError:   true,
			ErrorCode: normalizeErr.Code,
			Content:   normalizeErr.Message,
		}, nil
	}
	content, truncated := boundContent(content, maxCapturedBytes)
	return Result{
		Content:   content,
		IsError:   response.IsError,
		ErrorCode: errorCode(response.IsError),
		Truncated: truncated,
	}, nil
}

func (current *server) close(ctx context.Context) error {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.closeSession(ctx)
}

func (current *server) closeSession(ctx context.Context) error {
	session := current.session
	command := current.command
	current.session = nil
	current.command = nil
	if session == nil {
		current.terminateCommand(ctx, command)
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- session.Close()
	}()
	if command == nil || command.command == nil || command.command.Process == nil {
		select {
		case err := <-done:
			return normalizeCloseError(err)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var sessionErr error
	select {
	case sessionErr = <-done:
	case <-ctx.Done():
		_ = killMCPProcessGroup(command.command.Process.Pid)
		return ctx.Err()
	}

	timer := time.NewTimer(processTerminationGrace)
	defer timer.Stop()
	select {
	case waitErr := <-command.done:
		_ = signalMCPProcessGroup(command.command.Process.Pid)
		waitForMCPProcessGroup(ctx, command.command.Process.Pid, processTerminationGrace)
		_ = killMCPProcessGroup(command.command.Process.Pid)
		return errors.Join(normalizeCloseError(sessionErr), normalizeCloseError(waitErr))
	case <-timer.C:
		_ = signalMCPProcessGroup(command.command.Process.Pid)
	case <-ctx.Done():
		_ = killMCPProcessGroup(command.command.Process.Pid)
		return ctx.Err()
	}

	timer.Reset(processTerminationGrace)
	select {
	case waitErr := <-command.done:
		_ = killMCPProcessGroup(command.command.Process.Pid)
		return errors.Join(normalizeCloseError(sessionErr), normalizeCloseError(waitErr))
	case <-timer.C:
		_ = killMCPProcessGroup(command.command.Process.Pid)
	case <-ctx.Done():
		_ = killMCPProcessGroup(command.command.Process.Pid)
		return ctx.Err()
	}

	select {
	case waitErr := <-command.done:
		return errors.Join(normalizeCloseError(sessionErr), normalizeCloseError(waitErr))
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (current *server) terminateCommand(ctx context.Context, command *managedCommand) {
	if command == nil || command.command == nil || command.command.Process == nil {
		return
	}
	processGroupID := command.command.Process.Pid
	_ = signalMCPProcessGroup(processGroupID)
	timer := time.NewTimer(processTerminationGrace)
	defer timer.Stop()
	select {
	case <-command.done:
		waitForMCPProcessGroup(ctx, processGroupID, processTerminationGrace)
	case <-timer.C:
	case <-ctx.Done():
	}
	_ = killMCPProcessGroup(processGroupID)
}

func discoverTools(
	ctx context.Context,
	serverName string,
	session *mcp.ClientSession,
	redactor redactor,
) ([]frozenTool, error) {
	var discovered []frozenTool
	seen := make(map[string]struct{})
	totalDefinitionBytes := 0
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("discover tools: %w", err)
		}
		if len(discovered) >= maxDiscoveredTools {
			return nil, &Error{
				Code: "mcp_discovery_limit_exceeded",
				Message: fmt.Sprintf(
					"MCP server %q exposes more than %d tools",
					serverName,
					maxDiscoveredTools,
				),
			}
		}
		if tool == nil || !mcpToolNamePattern.MatchString(tool.Name) {
			return nil, &Error{
				Code: "mcp_invalid_tool_definition",
				Message: fmt.Sprintf(
					"MCP server %q returned a tool name outside the 1..128 [A-Za-z0-9_.-]+ constraint",
					serverName,
				),
			}
		}
		if redactor.containsSensitive(tool.Name) {
			return nil, &Error{
				Code:    "mcp_invalid_tool_definition",
				Message: fmt.Sprintf("MCP server %q returned a tool name containing a resolved sensitive value", serverName),
			}
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return nil, &Error{
				Code:    "mcp_invalid_tool_definition",
				Message: fmt.Sprintf("MCP server %q returned duplicate tool %q", serverName, tool.Name),
			}
		}
		seen[tool.Name] = struct{}{}
		description := strings.TrimSpace(tool.Description)
		if description == "" {
			description = fmt.Sprintf("%s %q from server %q.", fallbackDescriptionPrefix, tool.Name, serverName)
		}
		description = redactor.replace(description)
		if len(description) > maxToolDescriptionBytes {
			return nil, discoveryLimitError(
				serverName,
				tool.Name,
				"description",
				len(description),
				maxToolDescriptionBytes,
			)
		}
		rawSchema, marshalErr := json.Marshal(tool.InputSchema)
		if marshalErr != nil {
			return nil, &Error{
				Code:    "mcp_invalid_tool_definition",
				Message: fmt.Sprintf("MCP server %q tool %q schema cannot be encoded", serverName, tool.Name),
			}
		}
		if redactor.containsSensitive(string(rawSchema)) {
			return nil, &Error{
				Code: "mcp_invalid_tool_definition",
				Message: fmt.Sprintf(
					"MCP server %q tool %q schema contains a resolved sensitive value",
					serverName,
					tool.Name,
				),
			}
		}
		if tool.InputSchema != nil && len(rawSchema) > maxToolSchemaBytes {
			return nil, discoveryLimitError(
				serverName,
				tool.Name,
				"schema",
				len(rawSchema),
				maxToolSchemaBytes,
			)
		}
		schema, resolvedSchema, err := normalizeSchema(tool.InputSchema)
		if err != nil {
			return nil, &Error{
				Code:    "mcp_invalid_tool_definition",
				Message: fmt.Sprintf("MCP server %q tool %q: %v", serverName, tool.Name, err),
			}
		}
		if len(schema) > maxToolSchemaBytes {
			return nil, discoveryLimitError(
				serverName,
				tool.Name,
				"schema",
				len(schema),
				maxToolSchemaBytes,
			)
		}
		definitionBytes := len(tool.Name) + len(description) + len(schema)
		if definitionBytes > maxToolDefinitionBytes {
			return nil, discoveryLimitError(
				serverName,
				tool.Name,
				"definition",
				definitionBytes,
				maxToolDefinitionBytes,
			)
		}
		totalDefinitionBytes += definitionBytes
		if totalDefinitionBytes > maxTotalToolDefinitionBytes {
			return nil, &Error{
				Code: "mcp_discovery_limit_exceeded",
				Message: fmt.Sprintf(
					"MCP server %q tool definitions exceed the %d-byte total limit",
					serverName,
					maxTotalToolDefinitionBytes,
				),
			}
		}
		discovered = append(discovered, frozenTool{
			name: tool.Name,
			definition: runtimeapi.ToolDefinition{
				Name:        tool.Name,
				Description: description,
				InputSchema: schema,
			},
			schema: resolvedSchema,
		})
	}
	return discovered, nil
}

func normalizeSchema(schema any) (json.RawMessage, *jsonschema.Resolved, error) {
	if schema == nil {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, nil, fmt.Errorf("input schema cannot be encoded: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil || object == nil {
		return nil, nil, errors.New("input schema must be a top-level JSON object")
	}
	var validationSchema jsonschema.Schema
	if err := json.Unmarshal(encoded, &validationSchema); err != nil {
		return nil, nil, fmt.Errorf("input schema is invalid: %w", err)
	}
	resolved, err := validationSchema.Resolve(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("input schema is invalid: %w", err)
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, encoded); err != nil {
		return nil, nil, fmt.Errorf("input schema is malformed: %w", err)
	}
	return json.RawMessage(compacted.String()), resolved, nil
}

func discoveryLimitError(
	serverName string,
	toolName string,
	part string,
	actual int,
	limit int,
) *Error {
	return &Error{
		Code: "mcp_discovery_limit_exceeded",
		Message: fmt.Sprintf(
			"MCP server %q tool %q %s is %d bytes; limit is %d bytes",
			serverName,
			toolName,
			part,
			actual,
			limit,
		),
	}
}

type resultError struct {
	Code    string
	Message string
}

func normalizeResult(
	response *mcp.CallToolResult,
	redactor redactor,
) (string, *resultError) {
	if response == nil {
		return "", &resultError{Code: "mcp_invalid_response", Message: "MCP server returned an empty tool response"}
	}
	if response.StructuredContent != nil {
		encoded, err := json.Marshal(response.StructuredContent)
		if err != nil {
			return "", &resultError{Code: "mcp_invalid_response", Message: "MCP structured content could not be encoded"}
		}
		var object map[string]any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&object); err != nil || object == nil {
			return "", &resultError{Code: "mcp_invalid_response", Message: "MCP structured content must be a JSON object"}
		}
		redacted := redactStructuredValue(object, redactor)
		encoded, err = json.Marshal(redacted)
		if err != nil {
			return "", &resultError{Code: "mcp_invalid_response", Message: "MCP structured content could not be normalized"}
		}
		return string(encoded), nil
	}
	for _, item := range response.Content {
		if _, text := item.(*mcp.TextContent); !text {
			return "", &resultError{
				Code:    "mcp_unsupported_content",
				Message: "MCP tool returned unsupported non-text content",
			}
		}
	}
	text := make([]string, 0, len(response.Content))
	for _, item := range response.Content {
		text = append(text, redactor.replace(item.(*mcp.TextContent).Text))
	}
	return strings.Join(text, "\n"), nil
}

func redactStructuredValue(value any, redactor redactor) any {
	switch typed := value.(type) {
	case string:
		return redactor.replace(typed)
	case []any:
		for index := range typed {
			typed[index] = redactStructuredValue(typed[index], redactor)
		}
		return typed
	case map[string]any:
		for key, child := range typed {
			typed[key] = redactStructuredValue(child, redactor)
		}
		return typed
	default:
		return value
	}
}

func sameTools(left []frozenTool, right []frozenTool) bool {
	if len(left) != len(right) {
		return false
	}
	rightByName := make(map[string]runtimeapi.ToolDefinition, len(right))
	for _, tool := range right {
		rightByName[tool.name] = tool.definition
	}
	for _, tool := range left {
		candidate, exists := rightByName[tool.name]
		if !exists ||
			tool.definition.Description != candidate.Description ||
			string(tool.definition.InputSchema) != string(candidate.InputSchema) {
			return false
		}
	}
	return true
}

func operationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func validateArguments(
	name string,
	schema *jsonschema.Resolved,
	arguments json.RawMessage,
) error {
	var object map[string]any
	if len(arguments) == 0 || json.Unmarshal(arguments, &object) != nil || object == nil {
		return fmt.Errorf("arguments for MCP tool %q must be a JSON object", name)
	}
	if schema != nil {
		if err := schema.Validate(object); err != nil {
			return fmt.Errorf("arguments for MCP tool %q do not satisfy its frozen schema", name)
		}
	}
	return nil
}

type boundedHTTPTransport struct {
	base           http.RoundTripper
	endpointOrigin string
	headers        map[string]string
	maxWireBytes   int64
}

var errMCPHTTPWireLimit = errors.New("mcp_http_wire_limit_exceeded")

func (transport *boundedHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	if origin(cloned.URL) == transport.endpointOrigin {
		for name, value := range transport.headers {
			cloned.Header.Set(name, value)
		}
	} else {
		for name := range transport.headers {
			cloned.Header.Del(name)
		}
	}
	var cancel context.CancelFunc
	if cloned.Method == http.MethodDelete {
		closeCtx, closeCancel := context.WithTimeout(cloned.Context(), httpCloseRequestTimeout)
		cancel = closeCancel
		cloned = cloned.Clone(closeCtx)
	}
	response, err := transport.base.RoundTrip(cloned)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	wireLimit := transport.maxWireBytes
	if wireLimit <= 0 {
		wireLimit = maxHTTPWireBytes
	}
	if response.ContentLength > wireLimit {
		_ = response.Body.Close()
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf(
			"%w: MCP HTTP response Content-Length exceeds %d-byte wire limit",
			errMCPHTTPWireLimit,
			wireLimit,
		)
	}
	response.Body = &countingReadCloser{
		body:   response.Body,
		limit:  wireLimit,
		cancel: cancel,
	}
	return response, nil
}

type countingReadCloser struct {
	body      io.ReadCloser
	limit     int64
	read      int64
	exceeded  bool
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

func (reader *countingReadCloser) Read(buffer []byte) (int, error) {
	if reader.exceeded {
		return 0, errMCPHTTPWireLimit
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	remaining := reader.limit - reader.read
	maximumRead := int64(len(buffer))
	if maximumRead > remaining+1 {
		maximumRead = remaining + 1
	}
	if maximumRead < 1 {
		maximumRead = 1
	}
	count, err := reader.body.Read(buffer[:maximumRead])
	if int64(count) > remaining {
		allowed := int(max(remaining, 0))
		reader.read += int64(allowed)
		reader.exceeded = true
		return allowed, errMCPHTTPWireLimit
	}
	reader.read += int64(count)
	return count, err
}

func (reader *countingReadCloser) Close() error {
	reader.closeOnce.Do(func() {
		reader.closeErr = reader.body.Close()
		if reader.cancel != nil {
			reader.cancel()
		}
	})
	return reader.closeErr
}

func sameOriginRedirectPolicy(endpoint *url.URL) func(*http.Request, []*http.Request) error {
	expectedOrigin := origin(endpoint)
	return func(request *http.Request, via []*http.Request) error {
		if origin(request.URL) != expectedOrigin {
			return errors.New("MCP cross-origin redirect is not allowed")
		}
		if len(via) >= 10 {
			return errors.New("MCP redirect limit exceeded")
		}
		return nil
	}
}

func origin(value *url.URL) string {
	if value == nil {
		return ""
	}
	return strings.ToLower(value.Scheme) + "://" + strings.ToLower(value.Host)
}

func cloneStrings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for name, value := range values {
		cloned[name] = value
	}
	return cloned
}

type redactor struct {
	values []string
}

func newRedactor(values []string) redactor {
	cloned := append([]string(nil), values...)
	sort.Slice(cloned, func(left int, right int) bool {
		return len(cloned[left]) > len(cloned[right])
	})
	return redactor{values: cloned}
}

func (redactor redactor) clean(value string) string {
	return truncateUTF8(redactor.replace(value), maxExternalErrorBytes)
}

func (redactor redactor) replace(value string) string {
	for _, sensitive := range redactor.values {
		if sensitive != "" {
			value = strings.ReplaceAll(value, sensitive, "[REDACTED]")
		}
	}
	return value
}

func (redactor redactor) containsSensitive(value string) bool {
	for _, sensitive := range redactor.values {
		if sensitive == "" {
			continue
		}
		if strings.Contains(value, sensitive) {
			return true
		}
		encoded, _ := json.Marshal(sensitive)
		escaped := strings.Trim(string(encoded), `"`)
		if escaped != sensitive && strings.Contains(value, escaped) {
			return true
		}
	}
	return false
}

func (current *server) safeMessage(value string) string {
	return current.redactor.clean(value)
}

func (current *server) safeError(code string, prefix string, err error) *Error {
	message := prefix
	if err != nil {
		message += ": " + err.Error()
	}
	return &Error{Code: code, Message: current.safeMessage(message)}
}

type redactingLineWriter struct {
	mu         sync.Mutex
	sink       io.Writer
	redactor   redactor
	buffer     []byte
	discarding bool
}

func newRedactingLineWriter(sink io.Writer, redactor redactor) io.Writer {
	return &redactingLineWriter{sink: sink, redactor: redactor}
}

func (writer *redactingLineWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	originalLength := len(data)
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		var part []byte
		if newline < 0 {
			part = data
			data = nil
		} else {
			part = data[:newline+1]
			data = data[newline+1:]
		}
		if writer.discarding {
			if newline >= 0 {
				writer.discarding = false
			}
			continue
		}
		if len(writer.buffer)+len(part) > maxStderrLineBytes {
			writer.buffer = writer.buffer[:0]
			writer.discarding = newline < 0
			_, _ = io.WriteString(writer.sink, "MCP stderr line omitted: too long\n")
			continue
		}
		writer.buffer = append(writer.buffer, part...)
		if newline >= 0 {
			_, _ = io.WriteString(writer.sink, writer.redactor.clean(string(writer.buffer)))
			writer.buffer = writer.buffer[:0]
		}
	}
	return originalLength, nil
}

func sortedEnvironment(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment
}

func errorCode(isError bool) string {
	if isError {
		return "mcp_tool_error"
	}
	return ""
}

func normalizeCloseError(err error) error {
	var exitError *exec.ExitError
	if err == nil || errors.As(err, &exitError) {
		return nil
	}
	return err
}

func waitForMCPProcessGroup(ctx context.Context, processGroupID int, limit time.Duration) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	ticker := time.NewTicker(processGroupPollInterval)
	defer ticker.Stop()
	for {
		if !mcpProcessGroupExists(processGroupID) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
		}
	}
}

func boundContent(value string, maxBytes int64) (string, bool) {
	if int64(len(value)) <= maxBytes {
		return value, false
	}
	if !json.Valid([]byte(value)) {
		return truncateUTF8(value, maxBytes), true
	}
	const emptyPartial = `{"partial":""}`
	if maxBytes < int64(len(emptyPartial)) {
		return "{}", true
	}
	low, high := 0, len(value)
	best := emptyPartial
	for low <= high {
		middle := low + (high-low)/2
		prefix := truncateUTF8(value, int64(middle))
		encoded, _ := json.Marshal(struct {
			Partial string `json:"partial"`
		}{Partial: prefix})
		if int64(len(encoded)) <= maxBytes {
			best = string(encoded)
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best, true
}

func truncateUTF8(value string, maxBytes int64) string {
	if maxBytes <= 0 {
		return ""
	}
	if int64(len(value)) <= maxBytes {
		return value
	}
	end := int(maxBytes)
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}
