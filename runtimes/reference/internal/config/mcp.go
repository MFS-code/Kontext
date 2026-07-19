package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var mcpEnvironmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var reservedMCPHTTPHeaders = map[string]struct{}{
	"accept":               {},
	"connection":           {},
	"content-length":       {},
	"content-type":         {},
	"host":                 {},
	"keep-alive":           {},
	"last-event-id":        {},
	"mcp-method":           {},
	"mcp-name":             {},
	"mcp-protocol-version": {},
	"mcp-session-id":       {},
	"proxy-authenticate":   {},
	"proxy-authorization":  {},
	"proxy-connection":     {},
	"te":                   {},
	"trailer":              {},
	"transfer-encoding":    {},
	"upgrade":              {},
}

type MCPConfig struct {
	Servers []MCPServer `json:"servers"`
}

type MCPServer struct {
	Name            string            `json:"name"`
	Transport       string            `json:"transport"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	EnvFrom         map[string]string `json:"envFrom,omitempty"`
	Endpoint        string            `json:"endpoint,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	HeadersFromEnv  map[string]string `json:"headersFromEnv,omitempty"`
	Timeout         time.Duration     `json:"-"`
	MaxRetries      *int              `json:"maxRetries,omitempty"`
	SensitiveValues []string          `json:"-"`

	TimeoutValue string `json:"timeout,omitempty"`
}

func loadMCPConfig(getenv func(string) string) (MCPConfig, error) {
	inline := strings.TrimSpace(getenv("KONTEXT_MCP_CONFIG"))
	filename := strings.TrimSpace(getenv("KONTEXT_MCP_CONFIG_FILE"))
	if inline != "" && filename != "" {
		return MCPConfig{}, errors.New("KONTEXT_MCP_CONFIG and KONTEXT_MCP_CONFIG_FILE are mutually exclusive")
	}
	if inline == "" && filename == "" {
		return MCPConfig{}, nil
	}

	var data []byte
	if filename != "" {
		var err error
		data, err = os.ReadFile(filename)
		if err != nil {
			return MCPConfig{}, fmt.Errorf("read KONTEXT_MCP_CONFIG_FILE: %w", err)
		}
	} else {
		data = []byte(inline)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return MCPConfig{}, nil
	}

	var parsed MCPConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return MCPConfig{}, fmt.Errorf("decode MCP configuration: %w", err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return MCPConfig{}, err
	}
	if err := validateMCPConfig(&parsed, getenv); err != nil {
		return MCPConfig{}, err
	}
	return parsed, nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("decode MCP configuration: multiple JSON values are not allowed")
	}
	return fmt.Errorf("decode MCP configuration: %w", err)
}

func validateMCPConfig(config *MCPConfig, getenv func(string) string) error {
	seen := make(map[string]struct{}, len(config.Servers))
	for index := range config.Servers {
		server := &config.Servers[index]
		server.Name = strings.TrimSpace(server.Name)
		if server.Name == "" {
			return fmt.Errorf("MCP server %d: name is required", index)
		}
		if _, duplicate := seen[server.Name]; duplicate {
			return fmt.Errorf("MCP server name %q is duplicated", server.Name)
		}
		seen[server.Name] = struct{}{}

		if server.TimeoutValue != "" {
			timeout, err := time.ParseDuration(server.TimeoutValue)
			if err != nil || timeout < 0 {
				return fmt.Errorf("MCP server %q: timeout must be a non-negative Go duration", server.Name)
			}
			server.Timeout = timeout
		}
		if server.MaxRetries != nil && *server.MaxRetries < 0 {
			return fmt.Errorf("MCP server %q: maxRetries must be non-negative", server.Name)
		}

		switch server.Transport {
		case "stdio":
			if err := validateStdioServer(server, getenv); err != nil {
				return err
			}
		case "http":
			if err := validateHTTPServer(server, getenv); err != nil {
				return err
			}
		default:
			return fmt.Errorf("MCP server %q: transport must be \"stdio\" or \"http\"", server.Name)
		}
	}
	return nil
}

func validateStdioServer(server *MCPServer, getenv func(string) string) error {
	if !filepath.IsAbs(server.Command) || strings.ContainsRune(server.Command, '\x00') {
		return fmt.Errorf("MCP server %q: stdio command must be an absolute path without NUL bytes", server.Name)
	}
	if server.Endpoint != "" || len(server.Headers) > 0 || len(server.HeadersFromEnv) > 0 {
		return fmt.Errorf("MCP server %q: HTTP fields are not valid for stdio transport", server.Name)
	}
	if server.MaxRetries != nil {
		return fmt.Errorf("MCP server %q: maxRetries is only valid for HTTP transport", server.Name)
	}
	if server.Env == nil {
		server.Env = make(map[string]string)
	}
	for _, argument := range server.Args {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("MCP server %q: stdio arguments must not contain NUL bytes", server.Name)
		}
	}
	for childName, value := range server.Env {
		if !mcpEnvironmentNamePattern.MatchString(childName) {
			return fmt.Errorf("MCP server %q: child environment name %q is invalid", server.Name, childName)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("MCP server %q: child environment value for %q contains a NUL byte", server.Name, childName)
		}
	}
	for childName, parentName := range server.EnvFrom {
		if !mcpEnvironmentNamePattern.MatchString(childName) {
			return fmt.Errorf("MCP server %q: child environment name %q is invalid", server.Name, childName)
		}
		if _, literal := server.Env[childName]; literal {
			return fmt.Errorf("MCP server %q: child environment name %q is configured by both env and envFrom", server.Name, childName)
		}
		if !mcpEnvironmentNamePattern.MatchString(parentName) {
			return fmt.Errorf("MCP server %q: parent environment name %q is invalid", server.Name, parentName)
		}
		value := getenv(parentName)
		if value == "" {
			return fmt.Errorf("MCP server %q: referenced environment variable %q is missing", server.Name, parentName)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("MCP server %q: referenced environment variable %q contains a NUL byte", server.Name, parentName)
		}
		server.Env[childName] = value
		server.SensitiveValues = appendSensitiveValue(server.SensitiveValues, value)
	}
	return nil
}

func validateHTTPServer(server *MCPServer, getenv func(string) string) error {
	if server.Command != "" || len(server.Args) > 0 || len(server.Env) > 0 || len(server.EnvFrom) > 0 {
		return fmt.Errorf("MCP server %q: stdio fields are not valid for HTTP transport", server.Name)
	}
	endpoint, err := url.Parse(server.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return fmt.Errorf("MCP server %q: endpoint must be an absolute HTTP(S) URL", server.Name)
	}
	if endpoint.User != nil {
		return fmt.Errorf("MCP server %q: endpoint must not contain embedded credentials", server.Name)
	}
	if endpoint.RawQuery != "" {
		return fmt.Errorf("MCP server %q: endpoint must not contain a query string", server.Name)
	}
	if endpoint.Fragment != "" {
		return fmt.Errorf("MCP server %q: endpoint must not contain a fragment", server.Name)
	}
	if server.Headers == nil {
		server.Headers = make(map[string]string)
	}
	seenHeaders := make(map[string]struct{}, len(server.Headers)+len(server.HeadersFromEnv))
	for name, value := range server.Headers {
		if !validHTTPHeaderName(name) || containsNewline(value) {
			return fmt.Errorf("MCP server %q: header %q has an invalid name or value", server.Name, name)
		}
		normalized := strings.ToLower(name)
		if _, reserved := reservedMCPHTTPHeaders[normalized]; reserved {
			return fmt.Errorf("MCP server %q: header %q is owned by HTTP or MCP and cannot be configured", server.Name, name)
		}
		if _, duplicate := seenHeaders[normalized]; duplicate {
			return fmt.Errorf("MCP server %q: header %q is configured more than once", server.Name, name)
		}
		seenHeaders[normalized] = struct{}{}
	}
	for name, parentName := range server.HeadersFromEnv {
		if !validHTTPHeaderName(name) {
			return fmt.Errorf("MCP server %q: header name %q is invalid", server.Name, name)
		}
		normalized := strings.ToLower(name)
		if _, reserved := reservedMCPHTTPHeaders[normalized]; reserved {
			return fmt.Errorf("MCP server %q: header %q is owned by HTTP or MCP and cannot be configured", server.Name, name)
		}
		if _, duplicate := seenHeaders[normalized]; duplicate {
			return fmt.Errorf("MCP server %q: header %q is configured more than once", server.Name, name)
		}
		seenHeaders[normalized] = struct{}{}
		if !mcpEnvironmentNamePattern.MatchString(parentName) {
			return fmt.Errorf("MCP server %q: parent environment name %q is invalid", server.Name, parentName)
		}
		value := getenv(parentName)
		if value == "" {
			return fmt.Errorf("MCP server %q: referenced environment variable %q is missing", server.Name, parentName)
		}
		if containsNewline(value) {
			return fmt.Errorf("MCP server %q: referenced environment variable %q contains a newline", server.Name, parentName)
		}
		server.Headers[name] = value
		server.SensitiveValues = appendSensitiveValue(server.SensitiveValues, value)
	}
	return nil
}

func appendSensitiveValue(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func containsNewline(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) &&
			(character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}
