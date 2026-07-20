package procgroup

import (
	"context"
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
