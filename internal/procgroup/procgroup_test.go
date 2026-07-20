package procgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPrepareHardensOnlyCommandContext(t *testing.T) {
	plain := exec.Command("unused")
	Prepare(plain)
	if plain.Cancel != nil {
		t.Fatal("Prepare added Cancel to a plain command")
	}
	if plain.WaitDelay != 0 {
		t.Fatalf("plain command WaitDelay = %s, want zero", plain.WaitDelay)
	}

	command := exec.CommandContext(context.Background(), "unused")
	originalCancel := command.Cancel
	Prepare(command)
	if command.Cancel == nil {
		t.Fatal("Prepare removed CommandContext cancellation")
	}
	if command.WaitDelay != time.Second {
		t.Fatalf("CommandContext WaitDelay = %s, want 1s", command.WaitDelay)
	}
	if originalCancel == nil {
		t.Fatal("test command did not start with CommandContext cancellation")
	}
}

func TestPreparePreservesCallerWaitDelay(t *testing.T) {
	command := exec.CommandContext(context.Background(), "unused")
	command.WaitDelay = 250 * time.Millisecond
	Prepare(command)
	if command.WaitDelay != 250*time.Millisecond {
		t.Fatalf("WaitDelay = %s, want caller value", command.WaitDelay)
	}
}

func TestPrepareNilIsNoOp(t *testing.T) {
	Prepare(nil)
}

func TestTerminateStateMachineWaitsForGraceBeforeEscalating(t *testing.T) {
	events := make(chan string, 2)
	graceExpired := make(chan time.Time)
	result := make(chan error, 1)
	go func() {
		result <- terminate(
			context.Background(),
			123,
			nil,
			graceExpired,
			recordingTerminationOperations(events),
		)
	}()

	expectTerminationEvent(t, events, "signal")
	select {
	case event := <-events:
		t.Fatalf("unexpected event before grace expired: %s", event)
	default:
	}
	graceExpired <- time.Now()
	expectTerminationEvent(t, events, "kill")
	if err := <-result; err != nil {
		t.Fatalf("terminate after grace: %v", err)
	}
}

func TestTerminateStateMachineEscalatesAfterGracefulExit(t *testing.T) {
	events := make(chan string, 2)
	done := make(chan error)
	result := make(chan error, 1)
	go func() {
		result <- terminate(
			context.Background(),
			123,
			done,
			make(chan time.Time),
			recordingTerminationOperations(events),
		)
	}()

	expectTerminationEvent(t, events, "signal")
	done <- nil
	expectTerminationEvent(t, events, "kill")
	if err := <-result; err != nil {
		t.Fatalf("terminate after graceful exit: %v", err)
	}
}

func TestTerminateStateMachineCancellationEscalates(t *testing.T) {
	events := make(chan string, 2)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- terminate(
			ctx,
			123,
			nil,
			make(chan time.Time),
			recordingTerminationOperations(events),
		)
	}()

	expectTerminationEvent(t, events, "signal")
	cancel()
	expectTerminationEvent(t, events, "kill")
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("terminate canceled context = %v, want context.Canceled", err)
	}
}

func recordingTerminationOperations(events chan<- string) terminationOperations {
	return terminationOperations{
		signal: func(int, os.Signal) error {
			events <- "signal"
			return nil
		},
		kill: func(int) error {
			events <- "kill"
			return nil
		},
	}
}

func expectTerminationEvent(t *testing.T, events <-chan string, expected string) {
	t.Helper()
	select {
	case event := <-events:
		if event != expected {
			t.Fatalf("termination event = %q, want %q", event, expected)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for termination event %q", expected)
	}
}
