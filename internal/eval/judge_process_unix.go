//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package eval

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func hardenJudgeCommand(command *exec.Cmd) {
	command.WaitDelay = time.Second
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

func terminateJudgeCommand(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
