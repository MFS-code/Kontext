//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func preparePlatform(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
}

func signalProcessGroup(processGroupID int, signal os.Signal) error {
	if processGroupID <= 0 {
		return fmt.Errorf("process group ID must be positive: %d", processGroupID)
	}
	unixSignal, ok := signal.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported signal type %T", signal)
	}
	err := syscall.Kill(-processGroupID, unixSignal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killProcessGroup(processGroupID int) error {
	return signalProcessGroup(processGroupID, syscall.SIGKILL)
}

func cancelProcessGroup(processGroupID int) error {
	if processGroupID <= 0 {
		return fmt.Errorf("process group ID must be positive: %d", processGroupID)
	}
	err := syscall.Kill(-processGroupID, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func processGroupExists(processGroupID int) bool {
	if processGroupID <= 0 {
		return false
	}
	err := syscall.Kill(-processGroupID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func gracefulTerminationSignal() os.Signal {
	return syscall.SIGTERM
}
