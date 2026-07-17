package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
)

func TestLoadPreservesOpaqueModelAndParsesOptionalInputs(t *testing.T) {
	values := map[string]string{
		"KONTEXT_RUN_NAME":          "run-1",
		"KONTEXT_AGENT_NAME":        "agent-1",
		"KONTEXT_GOAL":              "explain the contract",
		"KONTEXT_PROVIDER":          "FAKE",
		"KONTEXT_MODEL":             "vendor/model@2026:beta",
		"KONTEXT_TOOLS":             " one, two ,,",
		"KONTEXT_BUDGET_TOKENS":     "123",
		"KONTEXT_BUDGET_WALLCLOCK":  "1m30s",
		"KONTEXT_BUDGET_DOLLARS":    "0",
		"KONTEXT_PROVIDER_ENDPOINT": "http://provider.default.svc:8080/v1",
	}
	loaded, err := config.Load(lookup(values))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.Model != "vendor/model@2026:beta" {
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
	if loaded.ProviderEndpoint != "http://provider.default.svc:8080/v1" {
		t.Fatalf("unexpected endpoint %q", loaded.ProviderEndpoint)
	}
}

func TestLoadLeavesLimitsDisabledWhenOmitted(t *testing.T) {
	loaded, err := config.Load(lookup(requiredValues()))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.TokenBudget != nil || loaded.WallclockBudget != nil || loaded.DollarBudget != nil {
		t.Fatalf("omitted limits must remain disabled: %#v", loaded)
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
		{name: "invalid endpoint", change: func(values map[string]string) { values["KONTEXT_PROVIDER_ENDPOINT"] = "localhost:8080" }},
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
