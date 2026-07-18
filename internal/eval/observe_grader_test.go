package eval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	eventv1alpha1 "github.com/kontext-dev/kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
)

func TestParseLogsCollectsBoundedMetadataAndEnvelope(t *testing.T) {
	var logs bytes.Buffer
	logs.WriteString(`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"lifecycle","data":{"phase":"started","secret":"do-not-record"}}` + "\n")
	logs.WriteString(`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:01Z","type":"tool","data":{"name":"shell","count":1,"isError":true,"errorCode":"denied","truncated":true,"output":"SECRET_VALUE"}}` + "\n")
	logs.WriteString(`KONTEXT_RESULT: {"ignored":"logs are not envelope authority"}` + "\n")

	parsed := ParseLogs(logs.Bytes(), false, map[eventv1alpha1.Type]struct{}{
		eventv1alpha1.TypeLifecycle: {},
		eventv1alpha1.TypeTool:      {},
	}, map[eventv1alpha1.Type]struct{}{
		eventv1alpha1.TypeLifecycle: {},
		eventv1alpha1.TypeTool:      {},
	})
	if parsed.Events.Counts[eventv1alpha1.TypeTool] != 1 ||
		len(parsed.Events.Tools) != 1 ||
		!parsed.Events.Tools[0].Truncated {
		t.Fatalf("unexpected event summary %#v", parsed.Events)
	}
	encoded, _ := json.Marshal(parsed.Events)
	if strings.Contains(string(encoded), "SECRET_VALUE") || strings.Contains(string(encoded), "do-not-record") {
		t.Fatalf("event summary retained unbounded/private event data: %s", encoded)
	}
}

func TestParseLogsFailsRelevantMalformedAndIncompleteEvents(t *testing.T) {
	validLifecycle := `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"lifecycle","data":{"phase":"started"}}`
	relevantLifecycle := map[eventv1alpha1.Type]struct{}{eventv1alpha1.TypeLifecycle: {}}
	oversized := strings.Repeat("x", MaxEventLineBytes+1) + "\n" + validLifecycle + "\n"
	parsed := ParseLogs([]byte(oversized), false, relevantLifecycle, nil)
	if len(parsed.Errors) == 0 || parsed.Events.Counts[eventv1alpha1.TypeLifecycle] != 0 {
		t.Fatalf("oversized line did not make event collection incomplete: %#v", parsed)
	}

	malformedTool := `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"tool","data":{"name":"shell","count":1,"truncated":false}}`
	parsed = ParseLogs(
		[]byte(malformedTool+"\n"),
		false,
		map[eventv1alpha1.Type]struct{}{eventv1alpha1.TypeTool: {}},
		nil,
	)
	if len(parsed.Errors) == 0 || parsed.Events.Counts[eventv1alpha1.TypeTool] != 0 {
		t.Fatalf("malformed tool event was accepted: %#v", parsed)
	}

	parsed = ParseLogs([]byte(validLifecycle+"\n"), true, relevantLifecycle, nil)
	if len(parsed.Errors) == 0 || !parsed.Events.Truncated {
		t.Fatalf("truncated log tail was accepted: %#v", parsed)
	}
}

func TestMalformedRelevantEventFallbackHandlesWhitespaceAndEscapedSlashes(t *testing.T) {
	line := "{\t\"apiVersion\"\t:\t\"kontext.dev\\/event\\/v1alpha1\",\t" +
		"\"type\"\t:\t\"tool\",\t\"data\":"
	parsed := ParseLogs(
		[]byte(line+"\n"),
		false,
		map[eventv1alpha1.Type]struct{}{eventv1alpha1.TypeTool: {}},
		nil,
	)
	if len(parsed.Errors) == 0 {
		t.Fatalf("malformed escaped event frame was ignored: %#v", parsed)
	}
}

func TestEventCountDoesNotRetainUnrequestedMetadata(t *testing.T) {
	tool := `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"tool","data":{"name":"shell","count":1,"isError":false,"errorCode":"","truncated":false,"output":"private"}}`
	parsed := ParseLogs(
		[]byte(tool+"\n"),
		false,
		map[eventv1alpha1.Type]struct{}{eventv1alpha1.TypeTool: {}},
		nil,
	)
	if parsed.Events.Counts[eventv1alpha1.TypeTool] != 1 {
		t.Fatalf("event was not counted: %#v", parsed)
	}
	if len(parsed.Events.Tools) != 0 || len(parsed.Events.Metadata) != 0 {
		t.Fatalf("unrequested event metadata was retained: %#v", parsed.Events)
	}
}

func TestGradeRecordCoversDeterministicGradersAndOptionalMetrics(t *testing.T) {
	zero64 := int64(0)
	exitCode := int32(7)
	turns := int32(2)
	toolCalls := int32(1)
	trueValue := true
	exact := "expected result"
	record := Record{
		TerminalPhase:  kontextv1alpha1.AgentRunPhaseFailed,
		StatusResult:   exact,
		StatusOutput:   &StatusOutput{MediaType: "application/json", Value: json.RawMessage(`{"ok":false}`)},
		StatusUsage:    &kontextv1alpha1.UsageStatus{Tokens: &zero64, InputTokens: &zero64},
		PodExitCode:    &exitCode,
		DurationMillis: 25,
		Envelope: &EnvelopeObservation{
			Outcome: resultv1alpha1.OutcomeFailed,
			Error:   &EnvelopeErrorObservation{Code: "denied"},
			Execution: &EnvelopeExecutionObservation{
				Model: "model-a", Turns: &turns, ToolCalls: &toolCalls,
			},
		},
		Events: EventSummary{
			Counts: map[eventv1alpha1.Type]int{eventv1alpha1.TypeTool: 1},
			Tools:  []ToolEvent{{Name: "shell", IsError: true, ErrorCode: "denied", Truncated: true}},
		},
	}
	graders := []Grader{
		{Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseFailed},
		{Type: GraderStatusResult, StatusResult: &StringMatch{Exact: &exact}},
		{Type: GraderStructuredOutput, StructuredOutput: &StructuredOutputExpectation{
			MediaType: "application/json", Present: &trueValue, Valid: &trueValue,
		}},
		{Type: GraderUsageFields, UsageFields: []string{"tokens", "inputTokens"}},
		{Type: GraderEnvelopeError, ErrorCode: "denied"},
		{Type: GraderEnvelopeOutcome, Outcome: resultv1alpha1.OutcomeFailed},
		{Type: GraderExecutionModel, Model: "model-a"},
		{Type: GraderEnvelopeTurns, Turns: &turns},
		{Type: GraderEnvelopeTools, ToolCalls: &toolCalls},
		{Type: GraderEventCount, Event: &EventCountExpectation{Type: eventv1alpha1.TypeTool, Count: 1}},
		{Type: GraderToolEvents, Tool: &ToolExpectation{
			Name: "shell", Count: 1, IsError: &trueValue, ErrorCode: "denied", Truncated: &trueValue,
		}},
		{Type: GraderDuration, MaxDuration: Duration{Duration: time.Second}},
		{Type: GraderPodExitCode, ExitCode: &exitCode},
	}
	GradeRecord(&record, graders)
	if !gradesPass(record.Grades) {
		t.Fatalf("expected all graders to pass: %#v", record.Grades)
	}
	presence := usagePresence(&record)
	if !presence["tokens"] || presence["outputTokens"] {
		t.Fatalf("nil/zero optional metric semantics lost: %#v", presence)
	}
}

func TestStatusResultMatchModes(t *testing.T) {
	contains := "needle"
	notContains := "secret"
	record := Record{StatusResult: "contains needle only"}
	GradeRecord(&record, []Grader{
		{Type: GraderStatusResult, StatusResult: &StringMatch{Contains: &contains}},
		{Type: GraderStatusResult, StatusResult: &StringMatch{NotContains: &notContains}},
	})
	if !gradesPass(record.Grades) {
		t.Fatalf("contains/not-contains graders failed: %#v", record.Grades)
	}
}
