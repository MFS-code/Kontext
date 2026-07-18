package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
)

func TestLoadPreservesOpaqueModelAndParsesOptionalInputs(t *testing.T) {
	values := map[string]string{
		"KONTEXT_RUN_NAME":                    "run-1",
		"KONTEXT_AGENT_NAME":                  "agent-1",
		"KONTEXT_GOAL":                        "explain the contract",
		"KONTEXT_PROVIDER":                    "FAKE",
		"KONTEXT_MODEL":                       " vendor/model@2026:beta ",
		"KONTEXT_TOOLS":                       " one, two ,,",
		"KONTEXT_BUDGET_TOKENS":               "123",
		"KONTEXT_BUDGET_WALLCLOCK":            "1m30s",
		"KONTEXT_BUDGET_DOLLARS":              "0",
		"KONTEXT_MAX_TURNS":                   "4",
		"KONTEXT_MAX_TOOL_CALLS":              "8",
		"KONTEXT_MAX_TOOL_RESULT_BYTES":       "1024",
		"KONTEXT_MAX_TOTAL_TOOL_OUTPUT_BYTES": "4096",
		"KONTEXT_EMIT_TOOL_OUTPUT":            "true",
		"KONTEXT_PROVIDER_ENDPOINT":           "http://provider.default.svc:8080/v1",
		"ANTHROPIC_API_KEY":                   "anthropic-secret",
		"OPENAI_API_KEY":                      "openai-secret",
	}
	loaded, err := config.Load(lookup(values))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.Model != " vendor/model@2026:beta " {
		t.Fatalf("model identifier changed: %q", loaded.Model)
	}
	if loaded.Provider != "fake" {
		t.Fatalf("expected normalized provider fake, got %q", loaded.Provider)
	}
	if strings.Join(loaded.Tools, ",") != "one,two" {
		t.Fatalf("unexpected tools %#v", loaded.Tools)
	}
	if loaded.TokenBudget == nil || *loaded.TokenBudget != 123 {
		t.Fatalf("unexpected token budget %v", loaded.TokenBudget)
	}
	if loaded.WallclockBudget == nil || *loaded.WallclockBudget != 90*time.Second {
		t.Fatalf("unexpected wallclock budget %v", loaded.WallclockBudget)
	}
	if loaded.DollarBudget == nil || *loaded.DollarBudget != 0 {
		t.Fatalf("expected measured zero dollar budget, got %v", loaded.DollarBudget)
	}
	if loaded.MaxTurns == nil || *loaded.MaxTurns != 4 ||
		loaded.MaxToolCalls == nil || *loaded.MaxToolCalls != 8 ||
		loaded.MaxToolResultBytes == nil || *loaded.MaxToolResultBytes != 1024 ||
		loaded.MaxTotalToolOutputBytes == nil || *loaded.MaxTotalToolOutputBytes != 4096 {
		t.Fatalf("unexpected tool limits: %#v", loaded)
	}
	if !loaded.EmitToolOutput {
		t.Fatal("expected tool output event opt-in")
	}
	if loaded.ProviderEndpoint != "http://provider.default.svc:8080/v1" {
		t.Fatalf("unexpected endpoint %q", loaded.ProviderEndpoint)
	}
	if loaded.AnthropicAPIKey != "anthropic-secret" ||
		loaded.OpenAIAPIKey != "openai-secret" {
		t.Fatalf("provider credentials were not loaded")
	}
}

func TestLoadLeavesLimitsDisabledWhenOmitted(t *testing.T) {
	loaded, err := config.Load(lookup(requiredValues()))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.TokenBudget != nil ||
		loaded.WallclockBudget != nil ||
		loaded.DollarBudget != nil ||
		loaded.MaxTurns != nil ||
		loaded.MaxToolCalls != nil ||
		loaded.MaxToolResultBytes != nil ||
		loaded.MaxTotalToolOutputBytes != nil {
		t.Fatalf("omitted limits must remain disabled: %#v", loaded)
	}
}

func TestLoadTreatsZeroLimitsAsDisabled(t *testing.T) {
	values := requiredValues()
	values["KONTEXT_BUDGET_TOKENS"] = "0"
	values["KONTEXT_BUDGET_WALLCLOCK"] = "0s"
	values["KONTEXT_MAX_TURNS"] = "0"
	values["KONTEXT_MAX_TOOL_CALLS"] = "0"
	values["KONTEXT_MAX_TOOL_RESULT_BYTES"] = "0"
	values["KONTEXT_MAX_TOTAL_TOOL_OUTPUT_BYTES"] = "0"
	loaded, err := config.Load(lookup(values))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.TokenBudget != nil ||
		loaded.WallclockBudget != nil ||
		loaded.MaxTurns != nil ||
		loaded.MaxToolCalls != nil ||
		loaded.MaxToolResultBytes != nil ||
		loaded.MaxTotalToolOutputBytes != nil {
		t.Fatalf("zero limits must remain disabled: %#v", loaded)
	}
}

func TestLoadValidatesRequiredAndOptionalConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "missing goal", change: func(values map[string]string) { delete(values, "KONTEXT_GOAL") }},
		{name: "missing provider", change: func(values map[string]string) { delete(values, "KONTEXT_PROVIDER") }},
		{name: "missing model", change: func(values map[string]string) { delete(values, "KONTEXT_MODEL") }},
		{name: "invalid tokens", change: func(values map[string]string) { values["KONTEXT_BUDGET_TOKENS"] = "zero" }},
		{name: "invalid wallclock", change: func(values map[string]string) { values["KONTEXT_BUDGET_WALLCLOCK"] = "5 minutes" }},
		{name: "negative dollars", change: func(values map[string]string) { values["KONTEXT_BUDGET_DOLLARS"] = "-1" }},
		{name: "NaN dollars", change: func(values map[string]string) { values["KONTEXT_BUDGET_DOLLARS"] = "NaN" }},
		{name: "infinite dollars", change: func(values map[string]string) { values["KONTEXT_BUDGET_DOLLARS"] = "+Inf" }},
		{name: "negative max turns", change: func(values map[string]string) { values["KONTEXT_MAX_TURNS"] = "-1" }},
		{name: "invalid max tool calls", change: func(values map[string]string) { values["KONTEXT_MAX_TOOL_CALLS"] = "many" }},
		{name: "oversized tool result limit", change: func(values map[string]string) {
			values["KONTEXT_MAX_TOOL_RESULT_BYTES"] = "8388609"
		}},
		{name: "invalid tool output opt-in", change: func(values map[string]string) {
			values["KONTEXT_EMIT_TOOL_OUTPUT"] = "sometimes"
		}},
		{name: "invalid endpoint", change: func(values map[string]string) { values["KONTEXT_PROVIDER_ENDPOINT"] = "localhost:8080" }},
		{name: "invalid base URL", change: func(values map[string]string) { values["KONTEXT_PROVIDER_BASE_URL"] = "localhost:8080" }},
		{name: "embedded endpoint credentials", change: func(values map[string]string) {
			values["KONTEXT_PROVIDER_ENDPOINT"] = "https://user:password@provider.example/messages"
		}},
		{name: "endpoint and base URL", change: func(values map[string]string) {
			values["KONTEXT_PROVIDER_ENDPOINT"] = "https://provider.example/messages"
			values["KONTEXT_PROVIDER_BASE_URL"] = "https://provider.example"
		}},
		{name: "invalid fake delay", change: func(values map[string]string) { values["KONTEXT_FAKE_DELAY"] = "-3s" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := requiredValues()
			test.change(values)
			if _, err := config.Load(lookup(values)); err == nil {
				t.Fatalf("expected configuration error")
			}
		})
	}
}

func TestLoadParsesProviderBaseURL(t *testing.T) {
	values := requiredValues()
	values["KONTEXT_PROVIDER_BASE_URL"] = "http://provider.default.svc:8080/openai"
	loaded, err := config.Load(lookup(values))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.ProviderBaseURL != "http://provider.default.svc:8080/openai" {
		t.Fatalf("unexpected base URL %q", loaded.ProviderBaseURL)
	}
}

func TestLoadParsesFakeDelay(t *testing.T) {
	values := requiredValues()
	values["KONTEXT_FAKE_SCENARIO"] = "delay"
	values["KONTEXT_FAKE_DELAY"] = "25ms"
	loaded, err := config.Load(lookup(values))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.FakeDelay != 25*time.Millisecond {
		t.Fatalf("unexpected fake delay %s", loaded.FakeDelay)
	}
}

func requiredValues() map[string]string {
	return map[string]string{
		"KONTEXT_GOAL":     "test goal",
		"KONTEXT_PROVIDER": "fake",
		"KONTEXT_MODEL":    "opaque-model",
	}
}

func lookup(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}
