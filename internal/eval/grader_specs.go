package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

type graderSpec struct {
	validate     func(Grader) error
	requirements func(Grader, *artifactRequirements)
	grade        func(*Record, Grader) Grade
}

type envelopeExpected func(Grader) (any, error)
type envelopeObserved func(*EnvelopeObservation) (any, bool)

func envelopeGraderSpec(
	expected envelopeExpected,
	observed envelopeObserved,
	selectArtifact func(*artifactRequirements),
) graderSpec {
	return graderSpec{
		validate: func(grader Grader) error {
			_, err := expected(grader)
			return err
		},
		requirements: func(_ Grader, requirements *artifactRequirements) {
			requirements.pod = true
			requirements.envelope = true
			selectArtifact(requirements)
		},
		grade: func(record *Record, grader Grader) Grade {
			expectedValue, err := expected(grader)
			result := Grade{Type: grader.Type, Expected: expectedValue}
			if err != nil {
				result.Message = err.Error()
				return result
			}
			observedValue, present := observed(record.Envelope)
			if present {
				result.Observed = observedValue
				result.Pass = observedValue == expectedValue
			}
			return result
		},
	}
}

var graderSpecs = map[GraderType]graderSpec{
	GraderTerminalPhase: {
		validate:     validateTerminalPhaseGrader,
		requirements: noArtifactRequirements,
		grade:        gradeTerminalPhase,
	},
	GraderStatusResult: {
		validate:     validateStatusResultGrader,
		requirements: requireStatusResult,
		grade:        gradeStatusResult,
	},
	GraderStructuredOutput: {
		validate:     validateStructuredOutputGrader,
		requirements: requireStatusOutput,
		grade:        gradeStructuredOutput,
	},
	GraderUsageFields: {
		validate:     validateUsageFieldsGrader,
		requirements: requireStatusUsage,
		grade:        gradeUsageFields,
	},
	GraderEnvelopeError: envelopeGraderSpec(
		func(grader Grader) (any, error) {
			if strings.TrimSpace(grader.ErrorCode) == "" {
				return nil, errors.New("errorCode is required")
			}
			return grader.ErrorCode, nil
		},
		func(envelope *EnvelopeObservation) (any, bool) {
			if envelope == nil || envelope.Error == nil {
				return nil, false
			}
			return envelope.Error.Code, true
		},
		func(requirements *artifactRequirements) {
			requirements.wantErrorCode = true
		},
	),
	GraderEnvelopeOutcome: envelopeGraderSpec(
		func(grader Grader) (any, error) {
			switch grader.Outcome {
			case resultv1alpha1.OutcomeSucceeded, resultv1alpha1.OutcomeFailed:
				return grader.Outcome, nil
			default:
				return nil, fmt.Errorf("invalid envelope outcome %q", grader.Outcome)
			}
		},
		func(envelope *EnvelopeObservation) (any, bool) {
			if envelope == nil {
				return nil, false
			}
			return envelope.Outcome, true
		},
		func(requirements *artifactRequirements) {
			requirements.wantOutcome = true
		},
	),
	GraderExecutionModel: envelopeGraderSpec(
		func(grader Grader) (any, error) {
			if strings.TrimSpace(grader.Model) == "" {
				return nil, errors.New("model is required")
			}
			return grader.Model, nil
		},
		func(envelope *EnvelopeObservation) (any, bool) {
			if envelope == nil || envelope.Execution == nil {
				return nil, false
			}
			return envelope.Execution.Model, true
		},
		func(requirements *artifactRequirements) {
			requirements.wantModel = true
		},
	),
	GraderEnvelopeTurns: envelopeGraderSpec(
		func(grader Grader) (any, error) {
			if grader.Turns == nil || *grader.Turns < 0 {
				return nil, errors.New("turns must be a non-negative integer")
			}
			return *grader.Turns, nil
		},
		func(envelope *EnvelopeObservation) (any, bool) {
			if envelope == nil || envelope.Execution == nil ||
				envelope.Execution.Turns == nil {
				return nil, false
			}
			return *envelope.Execution.Turns, true
		},
		func(requirements *artifactRequirements) {
			requirements.wantTurns = true
		},
	),
	GraderEnvelopeTools: envelopeGraderSpec(
		func(grader Grader) (any, error) {
			if grader.ToolCalls == nil || *grader.ToolCalls < 0 {
				return nil, errors.New("toolCalls must be a non-negative integer")
			}
			return *grader.ToolCalls, nil
		},
		func(envelope *EnvelopeObservation) (any, bool) {
			if envelope == nil || envelope.Execution == nil ||
				envelope.Execution.ToolCalls == nil {
				return nil, false
			}
			return *envelope.Execution.ToolCalls, true
		},
		func(requirements *artifactRequirements) {
			requirements.wantToolCalls = true
		},
	),
	GraderEventCount: {
		validate:     validateEventCountGrader,
		requirements: requireEventCount,
		grade:        gradeEventCount,
	},
	GraderToolEvents: {
		validate:     validateToolEventsGrader,
		requirements: requireToolEvents,
		grade:        gradeToolEvents,
	},
	GraderDuration: {
		validate:     validateDurationGrader,
		requirements: noArtifactRequirements,
		grade:        gradeDuration,
	},
	GraderPodExitCode: {
		validate:     validatePodExitCodeGrader,
		requirements: requirePodExitCode,
		grade:        gradePodExitCode,
	},
}

func graderSpecFor(graderType GraderType) (graderSpec, error) {
	spec, ok := graderSpecs[graderType]
	if !ok {
		return graderSpec{}, fmt.Errorf("unsupported grader type %q", graderType)
	}
	if spec.validate == nil || spec.requirements == nil || spec.grade == nil {
		return graderSpec{}, fmt.Errorf("grader type %q has an incomplete dispatch spec", graderType)
	}
	return spec, nil
}

func validateGrader(grader Grader) error {
	spec, err := graderSpecFor(grader.Type)
	if err != nil {
		return err
	}
	return spec.validate(grader)
}

func validateTerminalPhaseGrader(grader Grader) error {
	switch grader.Phase {
	case kontextv1alpha1.AgentRunPhaseSucceeded,
		kontextv1alpha1.AgentRunPhaseFailed,
		kontextv1alpha1.AgentRunPhaseBudgetExceeded:
		return nil
	default:
		return fmt.Errorf("invalid terminal phase %q", grader.Phase)
	}
}

func validateStatusResultGrader(grader Grader) error {
	if grader.StatusResult == nil {
		return errors.New("statusResult expectation is required")
	}
	count := 0
	if grader.StatusResult.Exact != nil {
		count++
	}
	if grader.StatusResult.Contains != nil {
		count++
	}
	if grader.StatusResult.NotContains != nil {
		count++
	}
	if count != 1 {
		return errors.New("statusResult requires exactly one of exact, contains, or notContains")
	}
	if grader.StatusResult.Contains != nil && *grader.StatusResult.Contains == "" {
		return errors.New("statusResult.contains must not be empty")
	}
	if grader.StatusResult.NotContains != nil && *grader.StatusResult.NotContains == "" {
		return errors.New("statusResult.notContains must not be empty")
	}
	return nil
}

func validateStructuredOutputGrader(grader Grader) error {
	if grader.StructuredOutput == nil {
		return errors.New("structuredOutput expectation is required")
	}
	mediaType := grader.StructuredOutput.MediaType
	if mediaType != "" && strings.TrimSpace(mediaType) == "" {
		return errors.New("structuredOutput.mediaType must not be blank")
	}
	if grader.StructuredOutput.Present == nil &&
		grader.StructuredOutput.Valid == nil &&
		mediaType == "" {
		return errors.New("structuredOutput requires present, valid, or mediaType")
	}
	return nil
}

func validateUsageFieldsGrader(grader Grader) error {
	if len(grader.UsageFields) == 0 {
		return errors.New("usageFields must not be empty")
	}
	seenFields := make(map[string]struct{}, len(grader.UsageFields))
	for _, field := range grader.UsageFields {
		switch field {
		case "tokens", "inputTokens", "outputTokens", "dollars":
		default:
			return fmt.Errorf("invalid usage field %q", field)
		}
		if _, exists := seenFields[field]; exists {
			return fmt.Errorf("duplicate usage field %q", field)
		}
		seenFields[field] = struct{}{}
	}
	return nil
}

func validateEventCountGrader(grader Grader) error {
	if grader.Event == nil || grader.Event.Count < 0 {
		return errors.New("event with non-negative count is required")
	}
	switch grader.Event.Type {
	case eventv1alpha1.TypeLifecycle, eventv1alpha1.TypeOutput, eventv1alpha1.TypeUsage,
		eventv1alpha1.TypeTool, eventv1alpha1.TypeError:
		return nil
	default:
		return fmt.Errorf("invalid event type %q", grader.Event.Type)
	}
}

func validateToolEventsGrader(grader Grader) error {
	if grader.Tool == nil {
		return errors.New("tool expectation is required")
	}
	if strings.TrimSpace(grader.Tool.Name) == "" {
		return errors.New("tool.name is required")
	}
	if grader.Tool.Count < 0 {
		return errors.New("tool.count must be non-negative")
	}
	if grader.Tool.ErrorCode != "" && strings.TrimSpace(grader.Tool.ErrorCode) == "" {
		return errors.New("tool.errorCode must not be blank")
	}
	return nil
}

func validateDurationGrader(grader Grader) error {
	if grader.MaxDuration.Duration <= 0 {
		return errors.New("maxDuration must be greater than zero")
	}
	return nil
}

func validatePodExitCodeGrader(grader Grader) error {
	if grader.ExitCode == nil {
		return errors.New("exitCode is required")
	}
	return nil
}

func noArtifactRequirements(Grader, *artifactRequirements) {}

func requireStatusResult(_ Grader, requirements *artifactRequirements) {
	requirements.statusResult = true
}

func requireStatusOutput(_ Grader, requirements *artifactRequirements) {
	requirements.statusOutput = true
}

func requireStatusUsage(_ Grader, requirements *artifactRequirements) {
	requirements.statusUsage = true
}

func requireEventCount(grader Grader, requirements *artifactRequirements) {
	requirements.pod = true
	requirements.logs = true
	requirements.eventTypes[grader.Event.Type] = struct{}{}
}

func requireToolEvents(_ Grader, requirements *artifactRequirements) {
	requirements.pod = true
	requirements.logs = true
	requirements.eventTypes[eventv1alpha1.TypeTool] = struct{}{}
	requirements.eventDetailTypes[eventv1alpha1.TypeTool] = struct{}{}
}

func requirePodExitCode(_ Grader, requirements *artifactRequirements) {
	requirements.pod = true
	requirements.exitCode = true
}

func gradeTerminalPhase(record *Record, grader Grader) Grade {
	return Grade{
		Type:     grader.Type,
		Expected: grader.Phase,
		Observed: record.TerminalPhase,
		Pass:     record.TerminalPhase == grader.Phase,
	}
}

func gradeStatusResult(record *Record, grader Grader) Grade {
	result := Grade{Type: grader.Type, Observed: record.StatusResult}
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
	return result
}

func gradeStructuredOutput(record *Record, grader Grader) Grade {
	expectation := grader.StructuredOutput
	present := record.StatusOutput != nil
	valid := present && json.Valid(record.StatusOutput.Value)
	mediaType := ""
	if present {
		mediaType = record.StatusOutput.MediaType
	}
	pass := true
	if expectation.Present != nil {
		pass = pass && present == *expectation.Present
	}
	if expectation.Valid != nil {
		pass = pass && valid == *expectation.Valid
	}
	if expectation.MediaType != "" {
		pass = pass && mediaType == expectation.MediaType
	}
	return Grade{
		Type:     grader.Type,
		Expected: expectation,
		Observed: map[string]any{"present": present, "valid": valid, "mediaType": mediaType},
		Pass:     pass,
	}
}

func gradeUsageFields(record *Record, grader Grader) Grade {
	observed := usagePresence(record)
	pass := true
	for _, field := range grader.UsageFields {
		pass = pass && observed[field]
	}
	return Grade{
		Type:     grader.Type,
		Expected: grader.UsageFields,
		Observed: observed,
		Pass:     pass,
	}
}

func gradeEventCount(record *Record, grader Grader) Grade {
	observed := record.Events.Counts[grader.Event.Type]
	return Grade{
		Type:     grader.Type,
		Expected: grader.Event.Count,
		Observed: observed,
		Pass:     observed == grader.Event.Count,
	}
}

func gradeToolEvents(record *Record, grader Grader) Grade {
	matches := matchingTools(record.Events.Tools, *grader.Tool)
	return Grade{
		Type:     grader.Type,
		Expected: grader.Tool,
		Observed: matches,
		Pass:     len(matches) == grader.Tool.Count,
	}
}

func gradeDuration(record *Record, grader Grader) Grade {
	expected := grader.MaxDuration.Milliseconds()
	return Grade{
		Type:     grader.Type,
		Expected: expected,
		Observed: record.DurationMillis,
		Pass:     record.DurationMillis <= expected,
	}
}

func gradePodExitCode(record *Record, grader Grader) Grade {
	result := Grade{Type: grader.Type, Expected: *grader.ExitCode}
	if record.PodExitCode != nil {
		result.Observed = *record.PodExitCode
		result.Pass = *record.PodExitCode == *grader.ExitCode
	}
	return result
}
