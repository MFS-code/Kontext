package mcpclient

import (
	"encoding/json"
	"strings"
	"testing"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

func TestNormalizeSchemaCompactsObjectSchema(t *testing.T) {
	schema, resolved, err := normalizeSchema(json.RawMessage(`{
		"type": "object",
		"properties": {"value": {"type": "string"}}
	}`))
	if err != nil {
		t.Fatalf("normalize schema: %v", err)
	}
	if resolved == nil || string(schema) != `{"type":"object","properties":{"value":{"type":"string"}}}` {
		t.Fatalf("unexpected normalized schema: %s resolved=%v", schema, resolved)
	}
}

func TestDiscoveryLimitErrorIdentifiesBoundedPart(t *testing.T) {
	err := discoveryLimitError("server", "tool", "schema", 11, 10)
	if err.Code != "mcp_discovery_limit_exceeded" ||
		!strings.Contains(err.Message, `"tool" schema is 11 bytes; limit is 10 bytes`) {
		t.Fatalf("unexpected discovery error: %#v", err)
	}
}

func TestSameToolsIgnoresDiscoveryOrderButDetectsDefinitionChanges(t *testing.T) {
	left := []frozenTool{
		frozenDefinition("one", "first", `{"type":"object"}`),
		frozenDefinition("two", "second", `{"type":"object"}`),
	}
	right := []frozenTool{left[1], left[0]}
	if !sameTools(left, right) {
		t.Fatal("equivalent reordered tools were treated as changed")
	}
	right[0].definition.Description = "changed"
	if sameTools(left, right) {
		t.Fatal("changed definition was treated as frozen")
	}
}

func frozenDefinition(name string, description string, schema string) frozenTool {
	return frozenTool{
		name: name,
		definition: runtimeapi.ToolDefinition{
			Name:        name,
			Description: description,
			InputSchema: json.RawMessage(schema),
		},
	}
}
