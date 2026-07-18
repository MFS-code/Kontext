package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

const shellTerminationGrace = 2 * time.Second

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type shellTool struct {
	stdout   io.Writer
	stderr   io.Writer
	maxBytes int64
}

type shellArguments struct {
	Command          string            `json:"command"`
	WorkingDirectory string            `json:"working_directory"`
	Environment      map[string]string `json:"environment,omitempty"`
}

type shellOutput struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func newShellTool(stdout io.Writer, stderr io.Writer, maxBytes int64) *shellTool {
	return &shellTool{
		stdout:   stdout,
		stderr:   stderr,
		maxBytes: maxBytes,
	}
}

func (tool *shellTool) Definition() runtimeapi.ToolDefinition {
	return runtimeapi.ToolDefinition{
		Name:        NameShell,
		Description: "Run one non-privileged shell command in an explicit working directory. Provider and Kubernetes credentials are not inherited.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"command":{"type":"string"},
				"working_directory":{"type":"string","description":"Absolute directory inside the runtime container"},
				"environment":{"type":"object","additionalProperties":{"type":"string"}}
			},
			"required":["command","working_directory"],
			"additionalProperties":false
		}`),
	}
}

func (tool *shellTool) Execute(
	ctx context.Context,
	rawArguments []byte,
) (outcome, error) {
	var arguments shellArguments
	if err := decodeArguments(rawArguments, &arguments); err != nil {
		return outcome{}, err
	}
	if strings.TrimSpace(arguments.Command) == "" ||
		strings.ContainsRune(arguments.Command, '\x00') {
		return outcome{}, &Error{
			Code:    "invalid_tool_arguments",
			Message: "command must be a non-empty string without NUL bytes",
		}
	}
	if !filepath.IsAbs(arguments.WorkingDirectory) {
		return outcome{}, &Error{
			Code:    "invalid_tool_arguments",
			Message: "working_directory must be absolute",
		}
	}
	info, err := os.Stat(arguments.WorkingDirectory)
	if err != nil {
		return outcome{}, &Error{
			Code:    "shell_working_directory_invalid",
			Message: fmt.Sprintf("inspect working directory: %v", err),
		}
	}
	if !info.IsDir() {
		return outcome{}, &Error{
			Code:    "shell_working_directory_invalid",
			Message: "working_directory must identify a directory",
		}
	}
	environment, err := filteredEnvironment(arguments.Environment)
	if err != nil {
		return outcome{}, err
	}

	captureBudget := &captureBudget{remaining: tool.maxBytes}
	stdoutCapture := &limitedBuffer{budget: captureBudget}
	stderrCapture := &limitedBuffer{budget: captureBudget}
	stdoutStream := &lineTerminatingWriter{sink: tool.stdout}
	stderrStream := &lineTerminatingWriter{sink: tool.stderr}
	defer stdoutStream.Finish()
	defer stderrStream.Finish()
	command := exec.Command("/bin/sh", "-c", arguments.Command)
	command.Dir = arguments.WorkingDirectory
	command.Env = environment
	command.Stdout = io.MultiWriter(stdoutStream, stdoutCapture)
	command.Stderr = io.MultiWriter(stderrStream, stderrCapture)
	prepareToolProcess(command)
	if err := command.Start(); err != nil {
		return outcome{}, &Error{
			Code:    "shell_start_failed",
			Message: fmt.Sprintf("start shell command: %v", err),
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		_ = signalToolProcessGroup(command.Process.Pid)
		timer := time.NewTimer(shellTerminationGrace)
		select {
		case waitErr = <-done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = killToolProcessGroup(command.Process.Pid)
			waitErr = <-done
		}
		_ = killToolProcessGroup(command.Process.Pid)
		return outcome{}, ctx.Err()
	}
	_ = killToolProcessGroup(command.Process.Pid)

	exitCode := 0
	if waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) {
			return outcome{}, &Error{
				Code:    "shell_wait_failed",
				Message: fmt.Sprintf("wait for shell command: %v", waitErr),
			}
		}
		exitCode = exitError.ExitCode()
	}
	encoded, err := json.Marshal(shellOutput{
		ExitCode: exitCode,
		Stdout:   stdoutCapture.String(),
		Stderr:   stderrCapture.String(),
	})
	if err != nil {
		return outcome{}, &Error{
			Code:    "shell_output_failed",
			Message: fmt.Sprintf("encode shell output: %v", err),
		}
	}
	result := outcome{
		Content:   string(encoded),
		Truncated: stdoutCapture.Truncated() || stderrCapture.Truncated(),
	}
	if exitCode != 0 {
		result.IsError = true
		result.ErrorCode = "shell_exit_nonzero"
	}
	return result, nil
}

func filteredEnvironment(requested map[string]string) ([]string, error) {
	values := map[string]string{
		"HOME":   "/tmp",
		"LANG":   "C.UTF-8",
		"PATH":   "/bin:/usr/bin",
		"TMPDIR": "/tmp",
	}
	for name, value := range requested {
		if !environmentNamePattern.MatchString(name) {
			return nil, &Error{
				Code:    "shell_environment_denied",
				Message: fmt.Sprintf("environment variable name %q is invalid", name),
			}
		}
		if sensitiveEnvironmentName(name) {
			return nil, &Error{
				Code:    "shell_environment_denied",
				Message: fmt.Sprintf("environment variable %q is reserved or sensitive", name),
			}
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, &Error{
				Code:    "shell_environment_denied",
				Message: fmt.Sprintf("environment variable %q contains a NUL byte", name),
			}
		}
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment, nil
}

func sensitiveEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range []string{
		"ANTHROPIC_",
		"AWS_",
		"AZURE_",
		"GOOGLE_",
		"KONTEXT_",
		"KUBERNETES_",
		"OPENAI_",
	} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	for _, fragment := range []string{
		"CREDENTIAL",
		"PASSWORD",
		"SECRET",
		"TOKEN",
	} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return strings.HasSuffix(upper, "_KEY")
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	budget    *captureBudget
	truncated bool
}

type lineTerminatingWriter struct {
	mu          sync.Mutex
	sink        io.Writer
	wrote       bool
	endsNewline bool
}

func (writer *lineTerminatingWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(data) > 0 {
		writer.wrote = true
		writer.endsNewline = data[len(data)-1] == '\n'
		_, _ = writer.sink.Write(data)
	}
	return len(data), nil
}

func (writer *lineTerminatingWriter) Finish() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.wrote && !writer.endsNewline {
		_, _ = writer.sink.Write([]byte{'\n'})
		writer.endsNewline = true
	}
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.budget.mu.Lock()
	defer buffer.budget.mu.Unlock()
	remaining := buffer.budget.remaining
	if remaining <= 0 {
		buffer.truncated = buffer.truncated || len(data) > 0
		return len(data), nil
	}
	toWrite := data
	if int64(len(toWrite)) > remaining {
		toWrite = toWrite[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(toWrite)
	buffer.budget.remaining -= int64(len(toWrite))
	return len(data), nil
}

func (buffer *limitedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (buffer *limitedBuffer) Truncated() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.truncated
}

type captureBudget struct {
	mu        sync.Mutex
	remaining int64
}
