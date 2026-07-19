package eval

import (
	"encoding/json"
	"fmt"
	"strings"
)

func GradeRecord(record *Record, graders []Grader) {
	record.Grades = make([]Grade, 0, len(graders))
	for _, grader := range graders {
		record.Grades = append(record.Grades, grade(record, grader))
	}
}

func grade(record *Record, grader Grader) Grade {
	result := Grade{Type: grader.Type}
	if err := validateGrader(grader); err != nil {
		result.Message = fmt.Sprintf("invalid grader: %v", err)
		return result
	}
	switch grader.Type {
	case GraderTerminalPhase:
		result.Expected = grader.Phase
		result.Observed = record.TerminalPhase
		result.Pass = record.TerminalPhase == grader.Phase
	case GraderStatusResult:
		result.Observed = record.StatusResult
		switch {
		case grader.StatusResult.Exact != nil:
			result.Expected = map[string]string{"exact": *grader.StatusResult.Exact}
			result.Pass = record.StatusResult == *grader.StatusResult.Exact
		case grader.StatusResult.Contains != nil:
			result.Expected = map[string]string{"contains": *grader.StatusResult.Contains}
			result.Pass = strings.Contains(record.StatusResult, *grader.StatusResult.Contains)
		case grader.StatusResult.NotContains != nil:
			result.Expected = map[string]string{"notContains": *grader.StatusResult.NotContains}
			result.Pass = !strings.Contains(record.StatusResult, *grader.StatusResult.NotContains)
		}
	case GraderStructuredOutput:
		expectation := grader.StructuredOutput
		present := record.StatusOutput != nil
		valid := present && json.Valid(record.StatusOutput.Value)
		mediaType := ""
		if present {
			mediaType = record.StatusOutput.MediaType
		}
		result.Expected = expectation
		result.Observed = map[string]any{"present": present, "valid": valid, "mediaType": mediaType}
		result.Pass = true
		if expectation.Present != nil {
			result.Pass = result.Pass && present == *expectation.Present
		}
		if expectation.Valid != nil {
			result.Pass = result.Pass && valid == *expectation.Valid
		}
		if expectation.MediaType != "" {
			result.Pass = result.Pass && mediaType == expectation.MediaType
		}
	case GraderUsageFields:
		observed := usagePresence(record)
		result.Expected = grader.UsageFields
		result.Observed = observed
		result.Pass = true
		for _, field := range grader.UsageFields {
			result.Pass = result.Pass && observed[field]
		}
	case GraderEnvelopeError:
		result.Expected = grader.ErrorCode
		if record.Envelope != nil && record.Envelope.Error != nil {
			result.Observed = record.Envelope.Error.Code
			result.Pass = record.Envelope.Error.Code == grader.ErrorCode
		}
	case GraderEnvelopeOutcome:
		result.Expected = grader.Outcome
		if record.Envelope != nil {
			result.Observed = record.Envelope.Outcome
			result.Pass = record.Envelope.Outcome == grader.Outcome
		}
	case GraderExecutionModel:
		result.Expected = grader.Model
		if record.Envelope != nil && record.Envelope.Execution != nil {
			result.Observed = record.Envelope.Execution.Model
			result.Pass = record.Envelope.Execution.Model == grader.Model
		}
	case GraderEnvelopeTurns:
		result.Expected = *grader.Turns
		if record.Envelope != nil && record.Envelope.Execution != nil &&
			record.Envelope.Execution.Turns != nil {
			result.Observed = *record.Envelope.Execution.Turns
			result.Pass = *record.Envelope.Execution.Turns == *grader.Turns
		}
	case GraderEnvelopeTools:
		result.Expected = *grader.ToolCalls
		if record.Envelope != nil && record.Envelope.Execution != nil &&
			record.Envelope.Execution.ToolCalls != nil {
			result.Observed = *record.Envelope.Execution.ToolCalls
			result.Pass = *record.Envelope.Execution.ToolCalls == *grader.ToolCalls
		}
	case GraderEventCount:
		result.Expected = grader.Event.Count
		result.Observed = record.Events.Counts[grader.Event.Type]
		result.Pass = record.Events.Counts[grader.Event.Type] == grader.Event.Count
	case GraderToolEvents:
		matches := matchingTools(record.Events.Tools, *grader.Tool)
		result.Expected = grader.Tool
		result.Observed = matches
		result.Pass = len(matches) == grader.Tool.Count
	case GraderDuration:
		result.Expected = grader.MaxDuration.Milliseconds()
		result.Observed = record.DurationMillis
		result.Pass = record.DurationMillis <= grader.MaxDuration.Milliseconds()
	case GraderPodExitCode:
		result.Expected = *grader.ExitCode
		if record.PodExitCode != nil {
			result.Observed = *record.PodExitCode
			result.Pass = *record.PodExitCode == *grader.ExitCode
		}
	default:
		result.Message = fmt.Sprintf("unsupported grader type %q", grader.Type)
	}
	if !result.Pass && result.Message == "" {
		result.Message = "observed value did not match expectation"
	}
	return result
}

func usagePresence(record *Record) map[string]bool {
	presence := map[string]bool{
		"tokens": false, "inputTokens": false, "outputTokens": false, "dollars": false,
	}
	if record.StatusUsage == nil {
		return presence
	}
	presence["tokens"] = record.StatusUsage.Tokens != nil
	presence["inputTokens"] = record.StatusUsage.InputTokens != nil
	presence["outputTokens"] = record.StatusUsage.OutputTokens != nil
	presence["dollars"] = record.StatusUsage.Dollars != nil
	return presence
}

func matchingTools(tools []ToolEvent, expectation ToolExpectation) []ToolEvent {
	var matches []ToolEvent
	for _, tool := range tools {
		if tool.Name != expectation.Name {
			continue
		}
		if expectation.IsError != nil && tool.IsError != *expectation.IsError {
			continue
		}
		if expectation.ErrorCode != "" && tool.ErrorCode != expectation.ErrorCode {
			continue
		}
		if expectation.Truncated != nil && tool.Truncated != *expectation.Truncated {
			continue
		}
		matches = append(matches, tool)
	}
	return matches
}

func gradesPass(grades []Grade) bool {
	for _, item := range grades {
		if !item.Pass {
			return false
		}
	}
	return true
}
