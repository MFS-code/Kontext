package main

import (
	"crypto/tls"
	"testing"
)

func TestDisableHTTP2ForcesHTTP11(t *testing.T) {
	cfg := &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	disableHTTP2(cfg)

	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "http/1.1" {
		t.Fatalf("expected NextProtos=[http/1.1], got %v", cfg.NextProtos)
	}
}

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("KONTEXT_REPORTER_IMAGE", "registry.example/reporter:v1")
	if got := envOrDefault("KONTEXT_REPORTER_IMAGE", "fallback"); got != "registry.example/reporter:v1" {
		t.Fatalf("expected configured reporter image, got %q", got)
	}
	if got := envOrDefault("KONTEXT_UNSET_TEST_VALUE", "fallback"); got != "fallback" {
		t.Fatalf("expected fallback value, got %q", got)
	}
}
