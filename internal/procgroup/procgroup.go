package procgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

const (
	commandWaitDelay = time.Second
)

// Prepare configures command so its descendants can be signaled as one unit.
// CommandContext commands also receive bounded pipe cleanup and group-aware
// cancellation. Plain commands keep Cancel nil so custom supervisors can wait
// and reap processes themselves.
func Prepare(command *exec.Cmd) {
	if command == nil {
		return
	}
	preparePlatform(command)
	if command.Cancel == nil {
		return
	}
	if command.WaitDelay == 0 {
		command.WaitDelay = commandWaitDelay
	}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return cancelProcessGroup(command.Process.Pid)
	}
}

// Signal sends signal to the process group identified by processGroupID.
// A group that has already exited is treated as successfully signaled.
func Signal(processGroupID int, signal os.Signal) error {
	return signalProcessGroup(processGroupID, signal)
}

// Kill forcefully terminates the process group identified by processGroupID.
// A group that has already exited is treated as successfully killed.
func Kill(processGroupID int) error {
	return killProcessGroup(processGroupID)
}

// Exists reports whether the process group is known to still exist.
func Exists(processGroupID int) bool {
	return processGroupExists(processGroupID)
}

// Terminate requests graceful termination, allows up to grace for the process
// group to exit, and always escalates to Kill. The graceful signal is
// best-effort. When escalation occurs before done reports a result, Terminate
// waits for done unless ctx is canceled.
func Terminate(
	ctx context.Context,
	processGroupID int,
	done <-chan error,
	grace time.Duration,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if grace < 0 {
		grace = 0
	}

	_ = Signal(processGroupID, gracefulTerminationSignal())

	timer := time.NewTimer(grace)
	defer timer.Stop()

	var (
		contextErr error
		waitErr    error
		waited     bool
	)
	select {
	case err, open := <-done:
		if open {
			waitErr = err
		}
		waited = true
	case <-timer.C:
	case <-ctx.Done():
		contextErr = ctx.Err()
	}

	killErr := Kill(processGroupID)
	if !waited && done != nil && contextErr == nil {
		select {
		case err, open := <-done:
			if open {
				waitErr = err
			}
		case <-ctx.Done():
			contextErr = ctx.Err()
		}
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		waitErr = nil
	}
	return errors.Join(contextErr, killErr, waitErr)
}
