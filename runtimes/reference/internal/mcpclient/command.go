package mcpclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MFS-code/Kontext/internal/environment"
	"github.com/MFS-code/Kontext/internal/procgroup"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
)

var errMCPStdioFrameLimit = errors.New("mcp_stdio_frame_limit_exceeded")
var errMCPStdioInvalidFrame = errors.New("mcp_stdio_invalid_frame")

type managedCommand struct {
	command *exec.Cmd
	done    chan error
}

func startCommandTransport(
	serverConfig config.MCPServer,
	stderr io.Writer,
	redactor redactor,
	frameLimit int64,
) (mcp.Transport, *managedCommand, error) {
	command := exec.Command(serverConfig.Command, serverConfig.Args...)
	command.Env = sortedEnvironment(serverConfig.Env)
	command.Stderr = newRedactingLineWriter(stderr, redactor)
	procgroup.Prepare(command)

	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("open MCP stdio stdout: %w", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, nil, fmt.Errorf("open MCP stdio stdin: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		_ = stdin.Close()
		return nil, nil, fmt.Errorf("start MCP stdio command: %w", err)
	}
	managed := &managedCommand{command: command, done: make(chan error, 1)}
	go func() {
		managed.done <- command.Wait()
		close(managed.done)
	}()
	return &mcp.IOTransport{
		Reader: newLineValidatingReadCloser(stdout, frameLimit),
		Writer: stdin,
	}, managed, nil
}

type lineValidatingReadCloser struct {
	reader       *bufio.Reader
	closer       io.Closer
	limit        int64
	pending      []byte
	offset       int
	afterPending error
	failed       error
	closeOnce    sync.Once
	closeErr     error
}

func newLineValidatingReadCloser(
	source io.ReadCloser,
	limit int64,
) *lineValidatingReadCloser {
	return &lineValidatingReadCloser{
		reader: bufio.NewReader(source),
		closer: source,
		limit:  limit,
	}
}

func (reader *lineValidatingReadCloser) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if reader.offset == len(reader.pending) {
		reader.pending = reader.pending[:0]
		reader.offset = 0
		if reader.afterPending != nil {
			err := reader.afterPending
			reader.afterPending = nil
			return 0, err
		}
	}
	if reader.failed != nil {
		return 0, reader.failed
	}
	if len(reader.pending) == 0 {
		if err := reader.loadLine(); err != nil {
			reader.failed = err
			return 0, err
		}
	}
	count := copy(buffer, reader.pending[reader.offset:])
	reader.offset += count
	return count, nil
}

func (reader *lineValidatingReadCloser) loadLine() error {
	lineBytes := int64(0)
	for {
		fragment, err := reader.reader.ReadSlice('\n')
		contentBytes := len(fragment)
		if len(fragment) > 0 && fragment[len(fragment)-1] == '\n' {
			contentBytes--
		}
		lineBytes += int64(contentBytes)
		if lineBytes > reader.limit {
			reader.pending = reader.pending[:0]
			return errMCPStdioFrameLimit
		}
		reader.pending = append(reader.pending, fragment...)

		switch {
		case err == nil:
			return reader.validatePendingLine()
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(reader.pending) == 0 {
				return io.EOF
			}
			reader.afterPending = io.EOF
			return reader.validatePendingLine()
		default:
			reader.pending = reader.pending[:0]
			return err
		}
	}
}

func (reader *lineValidatingReadCloser) validatePendingLine() error {
	content := reader.pending
	if len(content) > 0 && content[len(content)-1] == '\n' {
		content = content[:len(content)-1]
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	if !json.Valid(content) {
		reader.pending = reader.pending[:0]
		reader.afterPending = nil
		return errMCPStdioInvalidFrame
	}
	return nil
}

func (reader *lineValidatingReadCloser) Close() error {
	reader.closeOnce.Do(func() {
		reader.closeErr = reader.closer.Close()
	})
	return reader.closeErr
}

func sortedEnvironment(values map[string]string) []string {
	return environment.Sorted(values)
}
