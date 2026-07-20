package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MFS-code/Kontext/internal/procgroup"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

const (
	maxStdioFrameBytes      = int64(64 << 20)
	processTerminationGrace = 500 * time.Millisecond
)

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
			var clientError *runtimeapi.CodedError
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
				return nil, &runtimeapi.CodedError{
					Code: "mcp_discovery_limit_exceeded",
					Message: fmt.Sprintf(
						"configured MCP servers expose more than %d tools in total",
						maxDiscoveredTools,
					),
				}
			}
			if _, collision := manager.tools[discovered.name]; collision {
				manager.closeAfterStartupFailure()
				return nil, &runtimeapi.CodedError{
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
				return nil, &runtimeapi.CodedError{
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
) (runtimeapi.ToolResult, error) {
	if manager == nil {
		return runtimeapi.ToolResult{
			IsError:   true,
			ErrorCode: "mcp_unavailable",
			Content:   "MCP execution is unavailable",
		}, nil
	}
	selected, exists := manager.tools[name]
	if !exists {
		return runtimeapi.ToolResult{
			IsError:   true,
			ErrorCode: "unknown_tool",
			Content:   fmt.Sprintf("tool %q is not available", name),
		}, nil
	}
	if err := validateArguments(selected.name, selected.schema, arguments); err != nil {
		return runtimeapi.ToolResult{
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
		var clientError *runtimeapi.CodedError
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
		return &runtimeapi.CodedError{
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
) (runtimeapi.ToolResult, error) {
	current.mu.Lock()
	defer current.mu.Unlock()

	if current.stale {
		if err := current.closeSession(ctx); err != nil {
			return runtimeapi.ToolResult{
				IsError:   true,
				ErrorCode: "mcp_reconnect_failed",
				Content:   fmt.Sprintf("MCP server %q could not close its stale session", current.config.Name),
			}, nil
		}
		if err := current.connect(ctx); err != nil {
			current.stale = true
			return runtimeapi.ToolResult{
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
			return runtimeapi.ToolResult{}, ctx.Err()
		}
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return runtimeapi.ToolResult{
				IsError:   true,
				ErrorCode: "mcp_timeout",
				Content:   fmt.Sprintf("MCP tool %q timed out", name),
			}, nil
		}
		code := "mcp_protocol_error"
		if errors.Is(err, mcp.ErrConnectionClosed) {
			code = "mcp_transport_error"
		}
		return runtimeapi.ToolResult{
			IsError:   true,
			ErrorCode: code,
			Content: current.safeMessage(
				fmt.Sprintf("MCP tool %q failed: %v", name, err),
			),
		}, nil
	}

	content, truncated, normalizeErr := normalizeResult(response, current.redactor)
	if normalizeErr != nil {
		return runtimeapi.ToolResult{
			IsError:   true,
			ErrorCode: normalizeErr.Code,
			Content:   normalizeErr.Message,
		}, nil
	}
	return runtimeapi.ToolResult{
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
		current.terminateCommand(ctx, command)
		return ctx.Err()
	}

	terminationErr := procgroup.Terminate(
		ctx,
		command.command.Process.Pid,
		command.done,
		processTerminationGrace,
	)
	return errors.Join(normalizeCloseError(sessionErr), terminationErr)
}

func (current *server) terminateCommand(ctx context.Context, command *managedCommand) {
	if command == nil || command.command == nil || command.command.Process == nil {
		return
	}
	_ = procgroup.Terminate(
		ctx,
		command.command.Process.Pid,
		command.done,
		processTerminationGrace,
	)
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

func cloneStrings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for name, value := range values {
		cloned[name] = value
	}
	return cloned
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
