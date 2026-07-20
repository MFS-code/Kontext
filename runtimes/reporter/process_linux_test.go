//go:build linux

package main

import (
	"bytes"
	"testing"
)

func TestRunChildReapsOrphansWhileMainProcessRuns(t *testing.T) {
	script := `
orphan_pid=$(sh -c 'sleep 0.05 & echo $!')
sleep 0.3
if [ -e "/proc/${orphan_pid}/stat" ]; then
  echo "orphan ${orphan_pid} was not reaped" >&2
  exit 42
fi
`
	var stderr bytes.Buffer
	result, err := runChild(
		[]string{"sh", "-c", script},
		&bytes.Buffer{},
		&stderr,
		newCapture(CaptureFormatLastLine, defaultCaptureBytes),
		nil,
	)
	if err != nil {
		t.Fatalf("run child: %v (stderr: %s)", err, stderr.String())
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (stderr: %s)", result.ExitCode, stderr.String())
	}
}
