package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRunChildForwardsStreamsConcurrently(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	capture := newCapture(CaptureFormatLastLine, defaultCaptureBytes)
	result, err := runChild(
		context.Background(),
		[]string{"sh", "-c", `printf 'out-1\n'; printf 'err-1\n' >&2; printf 'out-2\n'; printf 'err-2\n' >&2`},
		&stdout,
		&stderr,
		capture,
		nil,
	)
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitCode)
	}
	if stdout.String() != "out-1\nout-2\n" {
		t.Fatalf("stdout changed: %q", stdout.String())
	}
	if stderr.String() != "err-1\nerr-2\n" {
		t.Fatalf("stderr changed: %q", stderr.String())
	}
	if got := string(capture.Result().Data); got != "out-2" {
		t.Fatalf("unexpected captured result %q", got)
	}
}

func TestRunChildForwardsTerminationSignals(t *testing.T) {
	tests := []struct {
		name     string
		signal   syscall.Signal
		exitCode int
	}{
		{name: "SIGTERM", signal: syscall.SIGTERM, exitCode: 143},
		{name: "SIGINT", signal: syscall.SIGINT, exitCode: 130},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout := newNotifyingBuffer("READY\n")
			signals := make(chan os.Signal, 1)
			type response struct {
				result ChildResult
				err    error
			}
			responseChannel := make(chan response, 1)
			go func() {
				result, err := runChild(
					context.Background(),
					[]string{"sh", "-c", "printf 'READY\\n'; exec sleep 30"},
					stdout,
					&bytes.Buffer{},
					newCapture(CaptureFormatLastLine, defaultCaptureBytes),
					signals,
				)
				responseChannel <- response{result: result, err: err}
			}()

			select {
			case <-stdout.Ready():
			case <-time.After(5 * time.Second):
				t.Fatalf("child did not become ready")
			}
			signals <- test.signal

			select {
			case response := <-responseChannel:
				if response.err != nil {
					t.Fatalf("run child: %v", response.err)
				}
				if response.result.ExitCode != test.exitCode {
					t.Fatalf("expected exit %d, got %d", test.exitCode, response.result.ExitCode)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("child did not exit after signal")
			}
		})
	}
}

func TestRunChildCleansUpRemainingProcessGroup(t *testing.T) {
	var stdout bytes.Buffer
	result, err := runChild(
		context.Background(),
		[]string{"sh", "-c", "sleep 30 & echo $!; exit 0"},
		&stdout,
		&bytes.Buffer{},
		newCapture(CaptureFormatLastLine, defaultCaptureBytes),
		nil,
	)
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitCode)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if err != nil {
		t.Fatalf("parse descendant pid from %q: %v", stdout.String(), err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived reporter cleanup", pid)
}

type notifyingBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	needle string
	ready  chan struct{}
	once   sync.Once
}

func newNotifyingBuffer(needle string) *notifyingBuffer {
	return &notifyingBuffer{
		needle: needle,
		ready:  make(chan struct{}),
	}
}

func (buffer *notifyingBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	written, err := buffer.buffer.Write(data)
	if strings.Contains(buffer.buffer.String(), buffer.needle) {
		buffer.once.Do(func() {
			close(buffer.ready)
		})
	}
	return written, err
}

func (buffer *notifyingBuffer) Ready() <-chan struct{} {
	return buffer.ready
}
