package eval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
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

func TestMalformedCurrentVersionEventMarkersFailCollection(t *testing.T) {
	tests := map[string]string{
		"literal slashes": `{"apiVersion": "kontext.dev/event/v1alpha1","type":"tool","data":`,
		"escaped slashes": `{"apiVersion":"kontext.dev\/event\/v1alpha1","type":"tool","data":`,
	}
	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			parsed := ParseLogs(
				[]byte(line+"\n"),
				false,
				map[eventv1alpha1.Type]struct{}{eventv1alpha1.TypeLifecycle: {}},
				nil,
			)
			if len(parsed.Errors) != 1 ||
				!strings.Contains(parsed.Errors[0], "parse required runtime event") {
				t.Fatalf("malformed current-version frame was ignored: %#v", parsed)
			}
		})
	}
}

func TestUnknownCurrentVersionEventTypeFailsCollection(t *testing.T) {
	currentUnknown := `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"telemetry","data":{}}`
	parsed := ParseLogs(
		[]byte(currentUnknown+"\n"),
		false,
		map[eventv1alpha1.Type]struct{}{eventv1alpha1.TypeLifecycle: {}},
		nil,
	)
	if len(parsed.Errors) != 1 ||
		!strings.Contains(parsed.Errors[0], `unsupported event type "telemetry"`) {
		t.Fatalf("unknown current-version type did not fail collection: %#v", parsed)
	}

	for _, version := range []string{"kontext.dev/event/v1alpha2", "kontext.dev/event/v1alpha10"} {
		futureUnknown := strings.Replace(currentUnknown, eventv1alpha1.APIVersion, version, 1)
		futureUnknown = strings.Replace(
			futureUnknown,
			`"data":{}`,
			`"data":{"mentionedVersion":"`+eventv1alpha1.APIVersion+`"}`,
			1,
		)
		parsed = ParseLogs(
			[]byte(futureUnknown+"\n"),
			false,
			map[eventv1alpha1.Type]struct{}{eventv1alpha1.TypeLifecycle: {}},
			nil,
		)
		if len(parsed.Errors) != 0 {
			t.Fatalf("%s line affected current-version collection: %#v", version, parsed)
		}
	}
}

func TestParseLogsValidatesAndSummarizesTypedEventData(t *testing.T) {
	lines := []string{
		`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"lifecycle","data":{"phase":"started","extension":true}}`,
		`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:01Z","type":"output","data":{"mediaType":"application/json","value":{"ok":true}}}`,
		`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:02Z","type":"usage","data":{"turn":1,"usage":{"tokens":3}}}`,
		`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:03Z","type":"tool","data":{"name":"shell","count":1,"isError":false,"truncated":false}}`,
		`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:04Z","type":"error","data":{"code":"denied","message":"no"}}`,
	}
	relevant := map[eventv1alpha1.Type]struct{}{
		eventv1alpha1.TypeLifecycle: {},
		eventv1alpha1.TypeOutput:    {},
		eventv1alpha1.TypeUsage:     {},
		eventv1alpha1.TypeTool:      {},
		eventv1alpha1.TypeError:     {},
	}
	parsed := ParseLogs(
		[]byte(strings.Join(lines, "\n")+"\n"),
		false,
		relevant,
		relevant,
	)
	if len(parsed.Errors) != 0 {
		t.Fatalf("typed event data was rejected: %v", parsed.Errors)
	}
	for eventType := range relevant {
		if parsed.Events.Counts[eventType] != 1 {
			t.Fatalf("%s count = %d, want 1", eventType, parsed.Events.Counts[eventType])
		}
	}
	if len(parsed.Events.Metadata) != len(lines) ||
		len(parsed.Events.Lifecycle) != 1 ||
		len(parsed.Events.Tools) != 1 ||
		len(parsed.Events.Errors) != 1 {
		t.Fatalf("typed summaries were incomplete: %#v", parsed.Events)
	}
}

func TestParseLogsRejectsMalformedTypedAndTopLevelEvents(t *testing.T) {
	tests := map[string]string{
		"strict top-level envelope": `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"tool","extra":true,"data":{"name":"shell","count":1,"isError":false,"truncated":false}}`,
		"lifecycle data":            `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"lifecycle","data":{"phase":""}}`,
		"output data":               `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"output","data":{"mediaType":"text/plain"}}`,
		"usage data":                `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"usage","data":{"turn":0,"usage":{}}}`,
		"tool data":                 `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"tool","data":{"name":"shell","count":1,"truncated":false}}`,
		"error data":                `{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"error","data":{"code":"denied"}}`,
	}
	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			parsed := ParseLogs(
				[]byte(line+"\n"),
				false,
				map[eventv1alpha1.Type]struct{}{
					eventv1alpha1.TypeLifecycle: {},
					eventv1alpha1.TypeOutput:    {},
					eventv1alpha1.TypeUsage:     {},
					eventv1alpha1.TypeTool:      {},
					eventv1alpha1.TypeError:     {},
				},
				nil,
			)
			if len(parsed.Errors) != 1 {
				t.Fatalf("malformed event did not fail clearly: %#v", parsed)
			}
		})
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

func TestGraderDispatchSpecsCoverEveryGraderType(t *testing.T) {
	exact := "result"
	present := true
	turns := int32(1)
	toolCalls := int32(1)
	exitCode := int32(0)
	graders := []Grader{
		{Type: GraderTerminalPhase, Phase: kontextv1alpha1.AgentRunPhaseSucceeded},
		{Type: GraderStatusResult, StatusResult: &StringMatch{Exact: &exact}},
		{Type: GraderStructuredOutput, StructuredOutput: &StructuredOutputExpectation{Present: &present}},
		{Type: GraderUsageFields, UsageFields: []string{"tokens"}},
		{Type: GraderEnvelopeError, ErrorCode: "denied"},
		{Type: GraderEnvelopeOutcome, Outcome: resultv1alpha1.OutcomeSucceeded},
		{Type: GraderExecutionModel, Model: "model"},
		{Type: GraderEnvelopeTurns, Turns: &turns},
		{Type: GraderEnvelopeTools, ToolCalls: &toolCalls},
		{Type: GraderEventCount, Event: &EventCountExpectation{
			Type: eventv1alpha1.TypeLifecycle, Count: 1,
		}},
		{Type: GraderToolEvents, Tool: &ToolExpectation{Name: "shell", Count: 1}},
		{Type: GraderDuration, MaxDuration: Duration{Duration: time.Second}},
		{Type: GraderPodExitCode, ExitCode: &exitCode},
	}
	if len(graderSpecs) != len(graders) {
		t.Fatalf("grader dispatch has %d specs, want %d", len(graderSpecs), len(graders))
	}
	for _, grader := range graders {
		t.Run(string(grader.Type), func(t *testing.T) {
			spec, err := graderSpecFor(grader.Type)
			if err != nil {
				t.Fatal(err)
			}
			if err := spec.validate(grader); err != nil {
				t.Fatalf("valid grader was rejected: %v", err)
			}
			requirements, err := requirementsForGraders([]Grader{grader})
			if err != nil {
				t.Fatalf("requirements dispatch failed: %v", err)
			}
			switch grader.Type {
			case GraderTerminalPhase, GraderDuration:
				if requirements.pod || requirements.logs || requirements.envelope ||
					requirements.statusResult || requirements.statusOutput || requirements.statusUsage {
					t.Fatalf("status-only grader requested artifacts: %#v", requirements)
				}
			case GraderStatusResult:
				if !requirements.statusResult {
					t.Fatal("status result was not requested")
				}
			case GraderStructuredOutput:
				if !requirements.statusOutput {
					t.Fatal("status output was not requested")
				}
			case GraderUsageFields:
				if !requirements.statusUsage {
					t.Fatal("status usage was not requested")
				}
			case GraderEnvelopeError, GraderEnvelopeOutcome, GraderExecutionModel,
				GraderEnvelopeTurns, GraderEnvelopeTools:
				if !requirements.pod || !requirements.envelope ||
					len(requirements.envelopeProjectors) != 1 {
					t.Fatalf("envelope artifacts were incomplete: %#v", requirements)
				}
			case GraderEventCount:
				if !requirements.pod || !requirements.logs ||
					len(requirements.eventTypes) != 1 {
					t.Fatalf("event artifacts were incomplete: %#v", requirements)
				}
			case GraderToolEvents:
				if !requirements.pod || !requirements.logs ||
					len(requirements.eventDetailTypes) != 1 {
					t.Fatalf("tool event artifacts were incomplete: %#v", requirements)
				}
			case GraderPodExitCode:
				if !requirements.pod || !requirements.exitCode {
					t.Fatalf("Pod exit artifacts were incomplete: %#v", requirements)
				}
			default:
				t.Fatalf("test is missing grader type %q", grader.Type)
			}
			_ = spec.grade(&Record{
				Events: EventSummary{Counts: map[eventv1alpha1.Type]int{}},
			}, grader)
		})
	}
	if _, err := requirementsForGraders([]Grader{{Type: GraderType("future")}}); err == nil ||
		!strings.Contains(err.Error(), "unsupported grader type") {
		t.Fatalf("unsupported requirements dispatch did not fail clearly: %v", err)
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

func TestGradeRecordRejectsUnsupportedGraderType(t *testing.T) {
	record := Record{}
	GradeRecord(&record, []Grader{{Type: GraderType("future")}})
	if len(record.Grades) != 1 ||
		record.Grades[0].Pass ||
		!strings.Contains(record.Grades[0].Message, "unsupported grader type") {
		t.Fatalf("unsupported grader was not rejected clearly: %#v", record.Grades)
	}
}
