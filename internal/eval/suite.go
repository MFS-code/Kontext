package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	eventv1alpha1 "github.com/kontext-dev/kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
)

const defaultTimeout = 5 * time.Minute

var invalidDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)

func LoadSuite(path string, runtimeImageOverride string) (EvalSuite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return EvalSuite{}, err
	}
	return ParseSuite(data, runtimeImageOverride)
}

func ParseSuite(data []byte, runtimeImageOverride string) (EvalSuite, error) {
	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return EvalSuite{}, fmt.Errorf("decode suite YAML: %w", err)
	}
	var suite EvalSuite
	decoder := json.NewDecoder(bytes.NewReader(jsonData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return EvalSuite{}, fmt.Errorf("decode suite: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return EvalSuite{}, errors.New("decode suite: trailing JSON value")
		}
		return EvalSuite{}, fmt.Errorf("decode suite trailing data: %w", err)
	}
	if err := prepareSuite(&suite, strings.TrimSpace(runtimeImageOverride)); err != nil {
		return EvalSuite{}, err
	}
	return suite, nil
}

func prepareSuite(suite *EvalSuite, runtimeImageOverride string) error {
	if suite.APIVersion != APIVersion {
		return fmt.Errorf("unsupported suite apiVersion %q", suite.APIVersion)
	}
	if suite.Kind != Kind {
		return fmt.Errorf("unsupported suite kind %q", suite.Kind)
	}
	if strings.TrimSpace(suite.Metadata.Name) == "" {
		return errors.New("metadata.name is required")
	}
	if len(suite.Spec.Cases) == 0 {
		return errors.New("spec.cases must not be empty")
	}
	if suite.Spec.Defaults.Timeout == nil {
		suite.Spec.Defaults.Timeout = &Duration{Duration: defaultTimeout}
	}
	if suite.Spec.Defaults.Timeout.Duration <= 0 {
		return errors.New("spec.defaults.timeout must be greater than zero")
	}
	if suite.Spec.Defaults.Namespace == "" {
		suite.Spec.Defaults.Namespace = "default"
	}
	seen := make(map[string]struct{}, len(suite.Spec.Cases))
	for index := range suite.Spec.Cases {
		item := &suite.Spec.Cases[index]
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			return fmt.Errorf("spec.cases[%d].id is required", index)
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("duplicate case id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Timeout == nil {
			item.Timeout = suite.Spec.Defaults.Timeout
		}
		if item.Timeout.Duration <= 0 {
			return fmt.Errorf("case %q timeout must be greater than zero", item.ID)
		}
		if runtimeImageOverride != "" {
			item.AgentRun.Runtime.Image = runtimeImageOverride
		} else if item.AgentRun.Runtime.Image == "" {
			item.AgentRun.Runtime.Image = suite.Spec.Defaults.RuntimeImage
		}
		if strings.TrimSpace(item.AgentRun.Runtime.Image) == "" {
			return fmt.Errorf("case %q requires agentRun.runtime.image", item.ID)
		}
		if strings.TrimSpace(item.AgentRun.Model) == "" {
			return fmt.Errorf("case %q requires agentRun.model", item.ID)
		}
		if strings.TrimSpace(item.AgentRun.Goal) == "" {
			return fmt.Errorf("case %q requires agentRun.goal", item.ID)
		}
		if len(item.Graders) == 0 {
			return fmt.Errorf("case %q requires at least one deterministic grader", item.ID)
		}
		for graderIndex, grader := range item.Graders {
			if err := validateGrader(grader); err != nil {
				return fmt.Errorf("case %q grader %d: %w", item.ID, graderIndex, err)
			}
		}
	}
	return nil
}

func validateGrader(grader Grader) error {
	switch grader.Type {
	case GraderTerminalPhase:
		switch grader.Phase {
		case kontextv1alpha1.AgentRunPhaseSucceeded,
			kontextv1alpha1.AgentRunPhaseFailed,
			kontextv1alpha1.AgentRunPhaseBudgetExceeded:
		default:
			return fmt.Errorf("invalid terminal phase %q", grader.Phase)
		}
	case GraderStatusResult:
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
	case GraderStructuredOutput:
		if grader.StructuredOutput == nil {
			return errors.New("structuredOutput expectation is required")
		}
		if grader.StructuredOutput.Present == nil &&
			grader.StructuredOutput.Valid == nil &&
			strings.TrimSpace(grader.StructuredOutput.MediaType) == "" {
			return errors.New("structuredOutput requires present, valid, or mediaType")
		}
		if grader.StructuredOutput.MediaType != "" &&
			strings.TrimSpace(grader.StructuredOutput.MediaType) == "" {
			return errors.New("structuredOutput.mediaType must not be blank")
		}
	case GraderUsageFields:
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
	case GraderEnvelopeError:
		if strings.TrimSpace(grader.ErrorCode) == "" {
			return errors.New("errorCode is required")
		}
	case GraderEnvelopeOutcome:
		switch grader.Outcome {
		case resultv1alpha1.OutcomeSucceeded, resultv1alpha1.OutcomeFailed:
		default:
			return fmt.Errorf("invalid envelope outcome %q", grader.Outcome)
		}
	case GraderExecutionModel:
		if strings.TrimSpace(grader.Model) == "" {
			return errors.New("model is required")
		}
	case GraderEnvelopeTurns:
		if grader.Turns == nil || *grader.Turns < 0 {
			return errors.New("turns must be a non-negative integer")
		}
	case GraderEnvelopeTools:
		if grader.ToolCalls == nil || *grader.ToolCalls < 0 {
			return errors.New("toolCalls must be a non-negative integer")
		}
	case GraderEventCount:
		if grader.Event == nil || grader.Event.Count < 0 {
			return errors.New("event with non-negative count is required")
		}
		switch grader.Event.Type {
		case eventv1alpha1.TypeLifecycle, eventv1alpha1.TypeOutput, eventv1alpha1.TypeUsage,
			eventv1alpha1.TypeTool, eventv1alpha1.TypeError:
		default:
			return fmt.Errorf("invalid event type %q", grader.Event.Type)
		}
	case GraderToolEvents:
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
	case GraderDuration:
		if grader.MaxDuration.Duration <= 0 {
			return errors.New("maxDuration must be greater than zero")
		}
	case GraderPodExitCode:
		if grader.ExitCode == nil {
			return errors.New("exitCode is required")
		}
	default:
		return fmt.Errorf("unsupported grader type %q", grader.Type)
	}
	return nil
}

func NameForCase(suiteName, caseID, invocation string) string {
	raw := suiteName + "\x00" + caseID + "\x00" + invocation
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:10]
	readable := strings.ToLower(strings.Join([]string{suiteName, caseID, invocation}, "-"))
	readable = strings.Trim(invalidDNSChars.ReplaceAllString(readable, "-"), "-")
	if readable == "" {
		readable = "eval"
	}
	maxReadable := 63 - len(digest) - 1
	if len(readable) > maxReadable {
		readable = strings.Trim(readable[:maxReadable], "-")
	}
	if readable == "" {
		readable = "eval"
	}
	return readable + "-" + digest
}
