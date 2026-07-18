//go:build !linux && !darwin

package mcpclient

import (
	"os"
	"os/exec"
)

func prepareMCPProcess(_ *exec.Cmd) {}

func signalMCPProcessGroup(processID int) error {
	process, err := os.FindProcess(processID)
	if err != nil {
		return err
	}
	return process.Kill()
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
