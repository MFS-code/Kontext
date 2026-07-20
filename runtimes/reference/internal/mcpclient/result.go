package mcpclient

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MFS-code/Kontext/internal/tooloutput"
)

const maxCapturedBytes = int64(8 << 20)

type resultError struct {
	Code    string
	Message string
}

func normalizeResult(
	response *mcp.CallToolResult,
	redactor redactor,
) (string, bool, *resultError) {
	if response == nil {
		return "", false, &resultError{
			Code:    "mcp_invalid_response",
			Message: "MCP server returned an empty tool response",
		}
	}
	if response.StructuredContent != nil {
		encoded, err := json.Marshal(response.StructuredContent)
		if err != nil {
			return "", false, &resultError{
				Code:    "mcp_invalid_response",
				Message: "MCP structured content could not be encoded",
			}
		}
		var object map[string]any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&object); err != nil || object == nil {
			return "", false, &resultError{
				Code:    "mcp_invalid_response",
				Message: "MCP structured content must be a JSON object",
			}
		}
		redacted := redactStructuredValue(object, redactor)
		encoded, err = json.Marshal(redacted)
		if err != nil {
			return "", false, &resultError{
				Code:    "mcp_invalid_response",
				Message: "MCP structured content could not be normalized",
			}
		}
		content, truncated := tooloutput.Bound(string(encoded), maxCapturedBytes)
		return content, truncated, nil
	}
	for _, item := range response.Content {
		if _, text := item.(*mcp.TextContent); !text {
			return "", false, &resultError{
				Code:    "mcp_unsupported_content",
				Message: "MCP tool returned unsupported non-text content",
			}
		}
	}
	text := make([]string, 0, len(response.Content))
	for _, item := range response.Content {
		text = append(text, redactor.replace(item.(*mcp.TextContent).Text))
	}
	content, truncated := tooloutput.Bound(strings.Join(text, "\n"), maxCapturedBytes)
	return content, truncated, nil
}
