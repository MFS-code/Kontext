package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const installFlag = "--install-to"

func installDestination(args []string) (string, bool, error) {
	if len(args) == 0 || args[0] != installFlag {
		return "", false, nil
	}
	if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
		return "", true, fmt.Errorf("%s requires exactly one destination path", installFlag)
	}
	return args[1], true, nil
}

func installCurrentExecutable(destination string) error {
	source, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate reporter executable: %w", err)
	}
	return installExecutable(source, destination)
}

func installExecutable(source string, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open reporter executable: %w", err)
	}
	defer input.Close()

	directory := filepath.Dir(destination)
	output, err := os.CreateTemp(directory, ".kontext-reporter-*")
	if err != nil {
		return fmt.Errorf("create reporter executable: %w", err)
	}
	temporaryPath := output.Name()
	defer os.Remove(temporaryPath)

	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copy reporter executable: %w", err)
	}
	if err := output.Chmod(0o755); err != nil {
		_ = output.Close()
		return fmt.Errorf("mark reporter executable: %w", err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return fmt.Errorf("sync reporter executable: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close reporter executable: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("install reporter executable: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	return nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open reporter directory: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("sync reporter directory: %w", err)
	}
	return nil
}
