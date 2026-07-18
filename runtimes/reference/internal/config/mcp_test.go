package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
)

func TestLoadMCPConfigResolvesReferencesWithoutInheritingEnvironment(t *testing.T) {
	values := requiredValues()
	values["MCP_TOKEN"] = "secret-token"
	values["MCP_CHILD_VALUE"] = "child-secret"
	values["KONTEXT_MCP_CONFIG"] = `{
		"servers": [
			{
				"name": "local",
				"transport": "stdio",
				"command": "/usr/bin/example",
				"args": ["--stdio"],
				"env": {"LITERAL": "safe"},
				"envFrom": {"TOKEN": "MCP_CHILD_VALUE"},
				"timeout": "250ms"
			},
			{
				"name": "remote",
				"transport": "http",
				"endpoint": "https://mcp.example/rpc",
				"headers": {"X-Literal": "safe"},
				"headersFromEnv": {"Authorization": "MCP_TOKEN"},
				"maxRetries": 2
			}
		]
	}`

	loaded, err := config.Load(lookup(values))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(loaded.MCP.Servers) != 2 {
		t.Fatalf("unexpected MCP servers: %#v", loaded.MCP.Servers)
	}
	if loaded.MCP.Servers[0].Timeout != 250*time.Millisecond ||
		loaded.MCP.Servers[0].Env["TOKEN"] != "child-secret" {
		t.Fatalf("stdio references were not resolved: %#v", loaded.MCP.Servers[0])
	}
	if _, inherited := loaded.MCP.Servers[0].Env["MCP_TOKEN"]; inherited {
		t.Fatal("runtime environment was inherited into stdio server")
	}
	if loaded.MCP.Servers[1].Headers["Authorization"] != "secret-token" ||
		loaded.MCP.Servers[1].MaxRetries == nil ||
		*loaded.MCP.Servers[1].MaxRetries != 2 {
		t.Fatalf("HTTP references were not resolved: %#v", loaded.MCP.Servers[1])
	}
	if len(loaded.MCP.Servers[0].SensitiveValues) != 1 ||
		len(loaded.MCP.Servers[1].SensitiveValues) != 1 {
		t.Fatal("resolved references were not marked for redaction")
	}
}

func TestLoadMCPConfigFileAndAmbiguity(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(filename, []byte(`{"servers":[]}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	values := requiredValues()
	values["KONTEXT_MCP_CONFIG_FILE"] = filename
	if _, err := config.Load(lookup(values)); err != nil {
		t.Fatalf("load file config: %v", err)
	}

	values["KONTEXT_MCP_CONFIG"] = `{"servers":[]}`
	if _, err := config.Load(lookup(values)); err == nil ||
		!strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected ambiguous source rejection, got %v", err)
	}
}

func TestLoadMCPConfigRejectsInvalidInputsWithoutSecretValues(t *testing.T) {
	tests := []struct {
		name   string
		config string
		values map[string]string
	}{
		{name: "unknown field", config: `{"servers":[],"extra":true}`},
		{name: "duplicate server", config: `{"servers":[{"name":"same","transport":"stdio","command":"/bin/a"},{"name":"same","transport":"stdio","command":"/bin/b"}]}`},
		{name: "relative command", config: `{"servers":[{"name":"local","transport":"stdio","command":"server"}]}`},
		{name: "stdio HTTP fields", config: `{"servers":[{"name":"local","transport":"stdio","command":"/bin/server","endpoint":"https://example.test"}]}`},
		{name: "stdio retries", config: `{"servers":[{"name":"local","transport":"stdio","command":"/bin/server","maxRetries":1}]}`},
		{name: "invalid child env", config: `{"servers":[{"name":"local","transport":"stdio","command":"/bin/server","env":{"BAD-NAME":"x"}}]}`},
		{name: "missing env", config: `{"servers":[{"name":"local","transport":"stdio","command":"/bin/server","envFrom":{"TOKEN":"MISSING_TOKEN"}}]}`},
		{name: "endpoint credentials", config: `{"servers":[{"name":"remote","transport":"http","endpoint":"https://user:password@example.test"}]}`},
		{name: "endpoint query", config: `{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test/mcp?token=bad"}]}`},
		{name: "endpoint fragment", config: `{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test/mcp#fragment"}]}`},
		{name: "HTTP stdio fields", config: `{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test","args":["bad"]}]}`},
		{name: "header newline", config: "{\"servers\":[{\"name\":\"remote\",\"transport\":\"http\",\"endpoint\":\"https://example.test\",\"headers\":{\"X-Test\":\"one\\ntwo\"}}]}"},
		{name: "malformed timeout", config: `{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test","timeout":"later"}]}`},
		{name: "negative timeout", config: `{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test","timeout":"-1s"}]}`},
		{name: "negative retries", config: `{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test","maxRetries":-1}]}`},
		{
			name:   "secret header newline",
			config: `{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test","headersFromEnv":{"Authorization":"TOKEN"}}]}`,
			values: map[string]string{"TOKEN": "secret-value\ninjected"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := requiredValues()
			values["KONTEXT_MCP_CONFIG"] = test.config
			for name, value := range test.values {
				values[name] = value
			}
			_, err := config.Load(lookup(values))
			if err == nil {
				t.Fatal("expected configuration error")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("error exposed a referenced secret: %v", err)
			}
		})
	}
}

func TestLoadMCPConfigRejectsHTTPAndMCPOwnedHeaders(t *testing.T) {
	headers := []string{
		"Host",
		"Content-Length",
		"Transfer-Encoding",
		"Connection",
		"Upgrade",
		"Accept",
		"Content-Type",
		"Mcp-Session-Id",
		"Mcp-Protocol-Version",
		"Mcp-Method",
		"Mcp-Name",
		"Last-Event-ID",
		"Proxy-Authorization",
		"Proxy-Authenticate",
		"Proxy-Connection",
		"Keep-Alive",
		"TE",
		"Trailer",
	}
	for _, header := range headers {
		for _, source := range []string{"literal", "environment"} {
			t.Run(header+"/"+source, func(t *testing.T) {
				values := requiredValues()
				values["RESERVED_HEADER_VALUE"] = "secret"
				if source == "literal" {
					values["KONTEXT_MCP_CONFIG"] = fmt.Sprintf(
						`{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test/mcp","headers":{%q:"value"}}]}`,
						strings.ToLower(header),
					)
				} else {
					values["KONTEXT_MCP_CONFIG"] = fmt.Sprintf(
						`{"servers":[{"name":"remote","transport":"http","endpoint":"https://example.test/mcp","headersFromEnv":{%q:"RESERVED_HEADER_VALUE"}}]}`,
						strings.ToUpper(header),
					)
				}
				_, err := config.Load(lookup(values))
				if err == nil || !strings.Contains(err.Error(), "owned by HTTP or MCP") {
					t.Fatalf("expected reserved header rejection, got %v", err)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatalf("reserved-header error exposed value: %v", err)
				}
			})
		}
	}
}
