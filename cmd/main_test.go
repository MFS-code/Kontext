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
