//go:build linux || darwin

package tools

import (
	"errors"
	"os/exec"
	"syscall"
)

func prepareToolProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalToolProcessGroup(processGroupID int) error {
	if err := syscall.Kill(-processGroupID, syscall.SIGTERM); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func killToolProcessGroup(processGroupID int) error {
	if err := syscall.Kill(-processGroupID, syscall.SIGKILL); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
