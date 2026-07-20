package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

const (
	maxDiscoveredTools          = 256
	maxToolDescriptionBytes     = 16 << 10
	maxToolSchemaBytes          = 256 << 10
	maxToolDefinitionBytes      = 280 << 10
	maxTotalToolDefinitionBytes = 4 << 20
	fallbackDescriptionPrefix   = "MCP tool"
)

var mcpToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type frozenTool struct {
	name       string
	definition runtimeapi.ToolDefinition
	schema     *jsonschema.Resolved
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
