package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
)

func TestWriteOutputsProducesJSONLAndSummary(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested")
	recordPath := filepath.Join(directory, "records.jsonl")
	summaryPath := filepath.Join(directory, "summary.json")
	records := []Record{
		{APIVersion: APIVersion, Kind: RecordKind, Suite: "s", CaseID: "a", Pass: true},
		{APIVersion: APIVersion, Kind: RecordKind, Suite: "s", CaseID: "b", Pass: false},
	}
	now := time.Now().UTC()
	summary := BuildSummary("s", now, now, records, recordPath)
	if err := WriteOutputs(recordPath, summaryPath, records, summary); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 2 {
		t.Fatalf("expected two JSONL records, got %d", lines)
	}
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Summary
	if err := json.Unmarshal(summaryData, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Total != 2 || decoded.Passed != 1 || decoded.Failed != 1 || decoded.RecordPath != recordPath {
		t.Fatalf("unexpected summary %#v", decoded)
	}
	for _, path := range []string{recordPath, summaryPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", path, permissions)
		}
	}
}

func TestCommandJudgeBoundsAndResponseValidation(t *testing.T) {
	judge := CommandJudge{Command: `printf '{"pass":true,"score":0.75,"rationale":"ok"}'`}
	result, err := judge.Evaluate(context.Background(), JudgeObservation{
		Suite: "s", CaseID: "c", Grades: []Grade{{Type: GraderTerminalPhase, Pass: true}},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Configured || !result.Pass || result.Score != 0.75 {
		t.Fatalf("unexpected judge result %#v", result)
	}
	_, err = judge.Evaluate(context.Background(), JudgeObservation{
		Suite: "s", CaseID: "c", StatusResult: strings.Repeat("x", MaxJudgeInputBytes),
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected bounded input error, got %v", err)
	}
	_, err = (CommandJudge{Command: `printf '{"pass":true}'`}).Evaluate(
		context.Background(),
		JudgeObservation{Suite: "s", CaseID: "c"},
	)
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("expected incomplete response error, got %v", err)
	}
}

func TestJudgeResultSerializesFalsePassAndZeroScore(t *testing.T) {
	encoded, err := json.Marshal(JudgeResult{
		Configured: true,
		Pass:       false,
		Score:      0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"pass":false`)) {
		t.Fatalf("false judge pass was omitted: %s", encoded)
	}
	if bytes.Contains(encoded, []byte(`"score"`)) {
		t.Fatalf("zero optional score should remain omitted: %s", encoded)
	}
}

func TestLimitedWriterReportsOutputLimit(t *testing.T) {
	var output bytes.Buffer
	writer := &limitedWriter{Writer: &output, Remaining: 3}
	written, err := writer.Write([]byte("hello"))
	if written != 3 || !errors.Is(err, errJudgeOutputLimit) {
		t.Fatalf("first write = (%d, %v), want (3, output limit)", written, err)
	}
	if output.String() != "hel" {
		t.Fatalf("bounded output = %q, want hel", output.String())
	}
	written, err = writer.Write([]byte("again"))
	if written != 0 || !errors.Is(err, errJudgeOutputLimit) {
		t.Fatalf("second write = (%d, %v), want (0, output limit)", written, err)
	}
}

func TestCommandJudgeDoesNotHangOnBackgroundPipeHolder(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "background-survived")
	startedAt := time.Now()
	_, err := (CommandJudge{
		Command: fmt.Sprintf(
			`(sleep 2; printf survived > %q) & printf '{"pass":true,"score":1,"rationale":"ok"}'`,
			marker,
		),
		Timeout: 5 * time.Second,
	}).Evaluate(context.Background(), JudgeObservation{
		Suite: "s", CaseID: "background-child",
	})
	if err == nil {
		t.Fatal("expected background pipe holder to be rejected")
	}
	if elapsed := time.Since(startedAt); elapsed > 3*time.Second {
		t.Fatalf("judge waited too long for background descendant: %s", elapsed)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("background judge descendant survived process-group termination: %v", statErr)
	}
}

func TestCommandJudgeKillsDetachedChildrenAfterSuccessfulExit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "detached-child-survived")
	result, err := (CommandJudge{
		Command: fmt.Sprintf(
			`(sleep 1; printf survived > %q) >/dev/null 2>&1 & printf '{"pass":true,"score":1,"rationale":"ok"}'`,
			marker,
		),
		Timeout: 5 * time.Second,
	}).Evaluate(context.Background(), JudgeObservation{
		Suite: "s", CaseID: "detached-child",
	})
	if err != nil || !result.Pass {
		t.Fatalf("successful judge response failed: result=%#v err=%v", result, err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("detached judge child survived successful parent exit: %v", statErr)
	}
}

func TestObservationContainsGradesButNoLogsOrEnvironment(t *testing.T) {
	observation := observationFor(Record{
		Suite:  "s",
		CaseID: "c",
		Grades: []Grade{{Type: GraderTerminalPhase, Pass: true}},
		Events: EventSummary{Counts: map[eventv1alpha1.Type]int{}},
	})
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, "deterministicGrades") ||
		strings.Contains(text, "kubeconfig") ||
		strings.Contains(text, "logs") {
		t.Fatalf("unsafe or unordered observation: %s", text)
	}
}
