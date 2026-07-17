//go:build !linux

package main

import (
	"errors"
	"syscall"
)

func prepareProcessSupervisor() error {
	return nil
}

func waitForMainProcess(mainPID int) (syscall.WaitStatus, error) {
	for {
		var status syscall.WaitStatus
		_, err := syscall.Wait4(mainPID, &status, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return status, err
	}
}

func reapRemainingProcesses() error {
	return nil
}
