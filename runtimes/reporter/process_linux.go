//go:build linux

package main

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const reapTimeout = 2 * time.Second

var (
	prepareOnce sync.Once
	prepareErr  error
)

func prepareProcessSupervisor() error {
	prepareOnce.Do(func() {
		prepareErr = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
	})
	return prepareErr
}

func waitForMainProcess(mainPID int) (syscall.WaitStatus, error) {
	for {
		var status syscall.WaitStatus
		waitedPID, err := syscall.Wait4(-1, &status, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if waitedPID == mainPID {
			return status, nil
		}
		// The reporter is a subreaper and may receive an orphaned descendant
		// before the main child exits. Waiting for any child reaps it here.
	}
}

func reapRemainingProcesses() error {
	deadline := time.Now().Add(reapTimeout)
	for {
		var status syscall.WaitStatus
		waitedPID, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		switch {
		case errors.Is(err, syscall.ECHILD):
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case err != nil:
			return fmt.Errorf("reap child processes: %w", err)
		case waitedPID > 0:
			continue
		case time.Now().After(deadline):
			return errors.New("timed out reaping child processes")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
