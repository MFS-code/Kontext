package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type ChildStartError struct {
	Err error
}

func (err *ChildStartError) Error() string {
	return fmt.Sprintf("start child process: %v", err.Err)
}

func (err *ChildStartError) Unwrap() error {
	return err.Err
}

type ChildResult struct {
	ExitCode int
}

func runChild(
	ctx context.Context,
	command []string,
	stdout io.Writer,
	stderr io.Writer,
	capture *Capture,
	signals <-chan os.Signal,
) (ChildResult, error) {
	if len(command) == 0 {
		return ChildResult{}, &ChildStartError{Err: errors.New("child command is empty")}
	}
	if err := prepareProcessSupervisor(); err != nil {
		return ChildResult{}, fmt.Errorf("prepare process supervisor: %w", err)
	}

	stdoutForwarder := newStreamForwarder(stdout, capture)
	stderrForwarder := newStreamForwarder(stderr, nil)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return ChildResult{}, &ChildStartError{Err: fmt.Errorf("open child stdout: %w", err)}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return ChildResult{}, &ChildStartError{Err: fmt.Errorf("open child stderr: %w", err)}
	}

	if err := cmd.Start(); err != nil {
		return ChildResult{}, &ChildStartError{Err: err}
	}
	processGroupID := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		_ = cleanupProcessGroup(processGroupID)
		return ChildResult{}, fmt.Errorf("release child process handle: %w", err)
	}

	var copies sync.WaitGroup
	copyErrors := make(chan error, 2)
	copies.Add(2)
	go copyChildStream(&copies, stdoutForwarder, stdoutPipe, copyErrors)
	go copyChildStream(&copies, stderrForwarder, stderrPipe, copyErrors)

	done := make(chan struct{})
	forwardingDone := make(chan struct{})
	signalErrors := &errorRecorder{}
	go func() {
		defer close(forwardingDone)
		forwardSignals(ctx, signals, processGroupID, done, signalErrors)
	}()

	status, waitErr := waitForMainProcess(processGroupID)
	close(done)
	<-forwardingDone

	cleanupErr := cleanupProcessGroup(processGroupID)
	reapErr := reapRemainingProcesses()
	copies.Wait()
	close(copyErrors)
	copyErr := errors.Join(channelErrors(copyErrors)...)
	result := ChildResult{ExitCode: exitCode(status)}

	if waitErr != nil {
		return result, fmt.Errorf("wait for child process: %w", waitErr)
	}
	if copyErr != nil {
		return result, fmt.Errorf("read child output: %w", copyErr)
	}
	if streamErr := errors.Join(stdoutForwarder.Err(), stderrForwarder.Err()); streamErr != nil {
		return result, fmt.Errorf("forward child output: %w", streamErr)
	}
	if signalErr := signalErrors.Err(); signalErr != nil {
		return result, signalErr
	}
	if cleanupErr != nil {
		return result, cleanupErr
	}
	if reapErr != nil {
		return result, reapErr
	}

	return result, nil
}

func copyChildStream(
	waitGroup *sync.WaitGroup,
	destination io.Writer,
	source io.Reader,
	copyErrors chan<- error,
) {
	defer waitGroup.Done()
	if _, err := io.Copy(destination, source); err != nil {
		copyErrors <- err
	}
}

func channelErrors(errorsChannel <-chan error) []error {
	var collected []error
	for err := range errorsChannel {
		collected = append(collected, err)
	}
	return collected
}

type streamForwarder struct {
	sink    io.Writer
	capture io.Writer

	mu  sync.Mutex
	err error
}

func newStreamForwarder(sink io.Writer, capture io.Writer) *streamForwarder {
	return &streamForwarder{sink: sink, capture: capture}
}

func (forwarder *streamForwarder) Write(data []byte) (int, error) {
	if forwarder.capture != nil {
		_, _ = forwarder.capture.Write(data)
	}

	forwarder.mu.Lock()
	defer forwarder.mu.Unlock()
	if forwarder.err == nil {
		if err := writeAll(forwarder.sink, data); err != nil {
			forwarder.err = err
		}
	}

	// Always report the input as consumed so os/exec continues draining the
	// child's pipe even if the log destination fails.
	return len(data), nil
}

func (forwarder *streamForwarder) Err() error {
	forwarder.mu.Lock()
	defer forwarder.mu.Unlock()
	return forwarder.err
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func forwardSignals(
	ctx context.Context,
	signals <-chan os.Signal,
	processGroupID int,
	done <-chan struct{},
	recorder *errorRecorder,
) {
	contextDone := ctx.Done()
	for {
		select {
		case <-done:
			return
		case signal, open := <-signals:
			if !open {
				signals = nil
				continue
			}
			if err := forwardSignal(processGroupID, signal); err != nil {
				recorder.Set(fmt.Errorf("forward signal %v: %w", signal, err))
			}
		case <-contextDone:
			if err := syscall.Kill(-processGroupID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
				recorder.Set(fmt.Errorf("forward context cancellation: %w", err))
			}
			contextDone = nil
		}
	}
}

func forwardSignal(processGroupID int, signal os.Signal) error {
	unixSignal, ok := signal.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported signal type %T", signal)
	}
	if err := syscall.Kill(-processGroupID, unixSignal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func cleanupProcessGroup(processGroupID int) error {
	if err := syscall.Kill(-processGroupID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("clean up child process group: %w", err)
	}
	return nil
}

func exitCode(status syscall.WaitStatus) int {
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return status.ExitStatus()
}

type errorRecorder struct {
	mu  sync.Mutex
	err error
}

func (recorder *errorRecorder) Set(err error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.err == nil {
		recorder.err = err
	}
}

func (recorder *errorRecorder) Err() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.err
}
