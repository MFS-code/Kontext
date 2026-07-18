package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

const (
	NameReadKnowledge  = "read_knowledge"
	NameKubernetesRead = "kubernetes_read"
	NameShell          = "shell"

	maxSafeCapturedBytes = int64(8 << 20)
)

type Config struct {
	Allowed       []string
	KnowledgeRoot string
	// MaxCapturedBytes overrides the fixed capture safety ceiling in tests.
	// Runtime-configured provider-output limits are enforced by the engine.
	MaxCapturedBytes int64
	Stdout           io.Writer
	Stderr           io.Writer
	Kubernetes       KubernetesConfig
}

type Registry struct {
	allowed     map[string]implementation
	known       map[string]struct{}
	definitions []runtimeapi.ToolDefinition
}

type implementation interface {
	Definition() runtimeapi.ToolDefinition
	Execute(context.Context, []byte) (outcome, error)
}

type outcome struct {
	Content   string
	IsError   bool
	ErrorCode string
	Truncated bool
}

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

func New(config Config) (*Registry, error) {
	if config.MaxCapturedBytes <= 0 {
		config.MaxCapturedBytes = maxSafeCapturedBytes
	}
	if config.MaxCapturedBytes > maxSafeCapturedBytes {
		return nil, &Error{
			Code: "invalid_tool_configuration",
			Message: fmt.Sprintf(
				"captured tool output cannot exceed %d bytes",
				maxSafeCapturedBytes,
			),
		}
	}
	if config.Stdout == nil {
		config.Stdout = io.Discard
	}
	if config.Stderr == nil {
		config.Stderr = io.Discard
	}

	implementations := map[string]implementation{
		NameReadKnowledge: newKnowledgeTool(config.KnowledgeRoot, config.MaxCapturedBytes),
		NameKubernetesRead: newKubernetesTool(
			config.Kubernetes,
			config.MaxCapturedBytes,
		),
		NameShell: newShellTool(
			config.Stdout,
			config.Stderr,
			config.MaxCapturedBytes,
		),
	}
	registry := &Registry{
		allowed: make(map[string]implementation, len(config.Allowed)),
		known:   make(map[string]struct{}, len(implementations)),
	}
	for name := range implementations {
		registry.known[name] = struct{}{}
	}
	for _, configuredName := range config.Allowed {
		name := strings.TrimSpace(configuredName)
		selected, exists := implementations[name]
		if !exists {
			return nil, &Error{
				Code:    "unknown_tool",
				Message: fmt.Sprintf("configured tool %q is not built into the reference runtime", name),
			}
		}
		if _, duplicate := registry.allowed[name]; duplicate {
			continue
		}
		registry.allowed[name] = selected
		registry.definitions = append(registry.definitions, selected.Definition())
	}
	return registry, nil
}

func (registry *Registry) Definitions() []runtimeapi.ToolDefinition {
	if registry == nil {
		return nil
	}
	return runtimeapi.CloneToolDefinitions(registry.definitions)
}

func (registry *Registry) Execute(
	ctx context.Context,
	call runtimeapi.ToolCall,
) (runtimeapi.ToolResult, error) {
	result := runtimeapi.ToolResult{
		CallID: call.ID,
		Name:   call.Name,
	}
	if registry == nil {
		result.IsError = true
		result.ErrorCode = "tool_unavailable"
		result.Content = "tool execution is unavailable"
		return result, nil
	}
	selected, allowed := registry.allowed[call.Name]
	if !allowed {
		result.IsError = true
		if _, known := registry.known[call.Name]; known {
			result.ErrorCode = "tool_denied"
			result.Content = fmt.Sprintf("tool %q is not allowed for this run", call.Name)
		} else {
			result.ErrorCode = "unknown_tool"
			result.Content = fmt.Sprintf("tool %q is not available", call.Name)
		}
		return result, nil
	}
	toolOutcome, err := selected.Execute(ctx, call.Arguments)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return runtimeapi.ToolResult{}, err
		}
		var toolError *Error
		if errors.As(err, &toolError) {
			result.IsError = true
			result.ErrorCode = toolError.Code
			result.Content = toolError.Message
			return result, nil
		}
		result.IsError = true
		result.ErrorCode = "tool_execution_failed"
		result.Content = err.Error()
		return result, nil
	}
	result.Content = toolOutcome.Content
	result.IsError = toolOutcome.IsError
	result.ErrorCode = toolOutcome.ErrorCode
	result.Truncated = toolOutcome.Truncated
	return result, nil
}
