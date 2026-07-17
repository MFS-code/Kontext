package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallDestination(t *testing.T) {
	destination, install, err := installDestination([]string{installFlag, "/kontext/bin/kontext-reporter"})
	if err != nil {
		t.Fatalf("parse install destination: %v", err)
	}
	if !install || destination != "/kontext/bin/kontext-reporter" {
		t.Fatalf("unexpected install mode: destination=%q install=%t", destination, install)
	}

	if _, install, err := installDestination([]string{"--", "agent"}); err != nil || install {
		t.Fatalf("normal child command must not select install mode")
	}
	if _, _, err := installDestination([]string{installFlag}); err == nil {
		t.Fatalf("expected missing destination error")
	}
}

func TestInstallExecutableCopiesAtomicallyAndMakesExecutable(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "bin", "kontext-reporter")
	if err := os.WriteFile(source, []byte("reporter binary"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.Mkdir(filepath.Dir(destination), 0o755); err != nil {
		t.Fatalf("create destination directory: %v", err)
	}

	if err := installExecutable(source, destination); err != nil {
		t.Fatalf("install executable: %v", err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read installed executable: %v", err)
	}
	if string(contents) != "reporter binary" {
		t.Fatalf("installed contents changed: %q", contents)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("stat installed executable: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("expected executable mode 0755, got %o", info.Mode().Perm())
	}
}
