//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package eval

import (
	"os/exec"
	"time"
)

func hardenJudgeCommand(command *exec.Cmd) {
	command.WaitDelay = time.Second
}

func terminateJudgeCommand(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
