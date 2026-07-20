//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package procgroup

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestPrepareCreatesProcessGroup(t *testing.T) {
	command := exec.Command("unused")
	Prepare(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatalf("Prepare did not enable Setpgid: %#v", command.SysProcAttr)
	}
}

func TestSignalKillAndExists(t *testing.T) {
	signaled, signaledDone := startReadyCommand(
		t,
		`trap 'exit 0' TERM; echo READY; while :; do :; done`,
	)
	if !Exists(signaled.Process.Pid) {
		t.Fatal("started process group does not exist")
	}
	if err := Signal(signaled.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("signal process group: %v", err)
	}
	if err := waitDone(t, signaledDone); err != nil {
		t.Fatalf("signaled process did not exit cleanly: %v", err)
	}

	killed, killedDone := startReadyCommand(
		t,
		`trap '' TERM; echo READY; while :; do sleep 1; done`,
	)
	if err := Kill(killed.Process.Pid); err != nil {
		t.Fatalf("kill process group: %v", err)
	}
	var exitError *exec.ExitError
	if err := waitDone(t, killedDone); !errors.As(err, &exitError) {
		t.Fatalf("killed process returned %v, want ExitError", err)
	}
}

func TestNonexistentProcessGroupIsTolerated(t *testing.T) {
	const nonexistentProcessGroup = 1 << 30
	if Exists(nonexistentProcessGroup) {
		t.Fatalf("unexpected process group %d", nonexistentProcessGroup)
	}
	if err := Signal(nonexistentProcessGroup, syscall.SIGTERM); err != nil {
		t.Fatalf("signal nonexistent process group: %v", err)
	}
	if err := Kill(nonexistentProcessGroup); err != nil {
		t.Fatalf("kill nonexistent process group: %v", err)
	}
	done := make(chan error)
	close(done)
	if err := Terminate(
		context.Background(),
		nonexistentProcessGroup,
		done,
		time.Second,
	); err != nil {
		t.Fatalf("terminate nonexistent process group: %v", err)
	}
}

func TestTerminateAllowsGracefulExit(t *testing.T) {
	command, done := startReadyCommand(
		t,
		`trap 'exit 0' TERM; echo READY; while :; do :; done`,
	)
	startedAt := time.Now()
	if err := Terminate(context.Background(), command.Process.Pid, done, time.Second); err != nil {
		t.Fatalf("terminate gracefully: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("graceful termination exhausted grace period: %s", elapsed)
	}
	if Exists(command.Process.Pid) {
		t.Fatal("process group survived graceful termination")
	}
}

func TestTerminateEscalatesAfterGrace(t *testing.T) {
	command, done := startReadyCommand(
		t,
		`trap '' TERM; echo READY; while :; do sleep 1; done`,
	)
	const grace = 100 * time.Millisecond
	startedAt := time.Now()
	if err := Terminate(context.Background(), command.Process.Pid, done, grace); err != nil {
		t.Fatalf("terminate after grace: %v", err)
	}
	elapsed := time.Since(startedAt)
	if elapsed < grace {
		t.Fatalf("kill escalated before grace elapsed: %s", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("kill escalation took too long: %s", elapsed)
	}
	if Exists(command.Process.Pid) {
		t.Fatal("process group survived kill escalation")
	}
}

func TestTerminateCancellationEscalatesImmediately(t *testing.T) {
	command, done := startReadyCommand(
		t,
		`trap '' TERM; echo READY; while :; do sleep 1; done`,
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startedAt := time.Now()
	err := Terminate(ctx, command.Process.Pid, done, 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("terminate canceled context = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("canceled termination took too long: %s", elapsed)
	}
	waitDone(t, done)
	if Exists(command.Process.Pid) {
		t.Fatal("process group survived canceled termination")
	}
}

func startReadyCommand(t *testing.T, script string) (*exec.Cmd, <-chan error) {
	t.Helper()
	command := exec.Command("sh", "-c", script)
	Prepare(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open stdout: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = Kill(command.Process.Pid)
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	if line != "READY\n" {
		t.Fatalf("readiness = %q, want READY", line)
	}
	return command, done
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit")
		return nil
	}
}
