//go:build linux || darwin

package mcpclient

import (
	"errors"
	"os/exec"
	"syscall"
)

func prepareMCPProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalMCPProcessGroup(processGroupID int) error {
	if err := syscall.Kill(-processGroupID, syscall.SIGTERM); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func killMCPProcessGroup(processGroupID int) error {
	if err := syscall.Kill(-processGroupID, syscall.SIGKILL); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func mcpProcessGroupExists(processGroupID int) bool {
	err := syscall.Kill(-processGroupID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
