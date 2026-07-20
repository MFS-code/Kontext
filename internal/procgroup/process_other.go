//go:build js || plan9 || wasip1 || windows

package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func preparePlatform(_ *exec.Cmd) {}

func signalProcessGroup(processID int, signal os.Signal) error {
	if processID <= 0 {
		return fmt.Errorf("process ID must be positive: %d", processID)
	}
	process, err := os.FindProcess(processID)
	if err != nil {
		return err
	}
	err = process.Signal(signal)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func killProcessGroup(processID int) error {
	if processID <= 0 {
		return fmt.Errorf("process ID must be positive: %d", processID)
	}
	process, err := os.FindProcess(processID)
	if err != nil {
		return err
	}
	err = process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func cancelProcessGroup(processID int) error {
	if processID <= 0 {
		return fmt.Errorf("process ID must be positive: %d", processID)
	}
	process, err := os.FindProcess(processID)
	if err != nil {
		return err
	}
	err = process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return os.ErrProcessDone
	}
	return err
}

func processGroupExists(_ int) bool {
	return false
}

func gracefulTerminationSignal() os.Signal {
	return os.Interrupt
}
