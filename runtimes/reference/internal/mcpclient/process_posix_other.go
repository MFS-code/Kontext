//go:build aix || android || dragonfly || freebsd || illumos || ios || netbsd || openbsd || solaris

package mcpclient

import (
	"os"
	"os/exec"
	"syscall"
)

func prepareMCPProcess(_ *exec.Cmd) {}

func signalMCPProcessGroup(processID int) error {
	process, err := os.FindProcess(processID)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}

func killMCPProcessGroup(processID int) error {
	process, err := os.FindProcess(processID)
	if err != nil {
		return err
	}
	return process.Kill()
}

func mcpProcessGroupExists(_ int) bool {
	return false
}
