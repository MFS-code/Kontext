//go:build !linux && !darwin

package tools

import (
	"os"
	"os/exec"
)

func prepareToolProcess(_ *exec.Cmd) {}

func signalToolProcessGroup(processID int) error {
	process, err := os.FindProcess(processID)
	if err != nil {
		return err
	}
	return process.Kill()
}

func killToolProcessGroup(processID int) error {
	process, err := os.FindProcess(processID)
	if err != nil {
		return err
	}
	return process.Kill()
}
