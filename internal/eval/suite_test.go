package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validSuiteYAML = `
apiVersion: kontext.dev/eval/v1alpha1
kind: EvalSuite
metadata:
  name: providers
spec:
  defaults:
    namespace: evals
    timeout: 30s
    runtimeImage: reference:dev
  cases:
    - id: prompt-model-a
      agentRun:
        goal: Say hello
        provider: fake
        model: model-a
        runtime: {}
      graders:
        - type: terminalPhase
          phase: Succeeded
    - id: prompt-model-b
      timeout: 10s
      agentRun:
        goal: Say hello
        provider: fake
        model: model-b
        runtime: {}
      graders:
        - type: eventCount
          event:
            type: tool
            count: 0
`

func TestParseSuiteStrictDefaultsAndSamePromptModels(t *testing.T) {
	suite, err := ParseSuite([]byte(validSuiteYAML), "override:dev")
	if err != nil {
		t.Fatalf("ParseSuite: %v", err)
	}
	if len(suite.Spec.Cases) != 2 {
		t.Fatalf("expected two cases, got %d", len(suite.Spec.Cases))
	}
	first, second := suite.Spec.Cases[0], suite.Spec.Cases[1]
	if first.AgentRun.Goal != second.AgentRun.Goal || first.AgentRun.Model == second.AgentRun.Model {
		t.Fatalf("same prompt/different model cases changed: %#v %#v", first.AgentRun, second.AgentRun)
	}
	if first.AgentRun.Runtime.Image != "override:dev" || second.AgentRun.Runtime.Image != "override:dev" {
		t.Fatalf("runtime image override was not applied")
	}
	if first.Timeout.Duration != 30*time.Second || second.Timeout.Duration != 10*time.Second {
		t.Fatalf("unexpected timeouts: %s and %s", first.Timeout, second.Timeout)
	}
}

func TestRepositoryKeylessSuiteParsesAndKeepsPerCaseImages(t *testing.T) {
	path := filepath.Join("..", "..", "evals", "suites", "keyless.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read keyless suite: %v", err)
	}
	suite, err := ParseSuite(data, "")
	if err != nil {
		t.Fatalf("parse keyless suite: %v", err)
	}
	if len(suite.Spec.Cases) != 10 {
		t.Fatalf("expected 10 keyless cases, got %d", len(suite.Spec.Cases))
	}
	if len(suite.Spec.Assertions) != 2 ||
		suite.Spec.Assertions[0].Type != SuiteAssertionFieldsEqual ||
		suite.Spec.Assertions[1].Type != SuiteAssertionForbiddenMarkers {
		t.Fatalf("keyless suite assertions changed: %#v", suite.Spec.Assertions)
	}

	var availableNotUsed, crash *Case
	for index := range suite.Spec.Cases {
		item := &suite.Spec.Cases[index]
		switch item.ID {
		case "read-knowledge-available-not-used":
			availableNotUsed = item
		case "reporter-process-crash":
			crash = item
		}
	}
	if availableNotUsed == nil ||
		len(availableNotUsed.AgentRun.Tools) != 1 ||
		availableNotUsed.AgentRun.Tools[0] != "read_knowledge" {
		t.Fatalf("available-but-unused tool case lost spec.tools: %#v", availableNotUsed)
	}
	if crash == nil || crash.AgentRun.Runtime.Image != "kontext-stdout-fixture:dev" {
		t.Fatalf("crash case lost its fixture image: %#v", crash)
	}
}

func TestParseSuiteRejectsInvalidInput(t *testing.T) {
	tests := map[string]string{
		"unknown field": strings.Replace(validSuiteYAML, "metadata:\n", "unexpected: true\nmetadata:\n", 1),
		"duplicate IDs": strings.Replace(validSuiteYAML, "prompt-model-b", "prompt-model-a", 1),
		"zero timeout":  strings.Replace(validSuiteYAML, "timeout: 30s", "timeout: 0s", 1),
		"invalid phase": strings.Replace(validSuiteYAML, "phase: Succeeded", "phase: Running", 1),
		"invalid tool": strings.Replace(
			validSuiteYAML,
			"name: providers",
			"name: providers",
			1,
		) + "\n",
	}
	tests["invalid tool"] = strings.Replace(
		validSuiteYAML,
		"type: terminalPhase\n          phase: Succeeded",
		"type: toolEvents\n          tool:\n            name: \"\"\n            count: -1",
		1,
	)
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSuite([]byte(input), ""); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestParseSuiteRejectsEmptyCasesAndMissingRequiredSpec(t *testing.T) {
	for name, input := range map[string]string{
		"empty": `
apiVersion: kontext.dev/eval/v1alpha1
kind: EvalSuite
metadata: {name: empty}
spec: {defaults: {}, cases: []}
`,
		"missing goal":  strings.Replace(validSuiteYAML, "goal: Say hello", "goal: \"\"", 1),
		"missing model": strings.Replace(validSuiteYAML, "model: model-a", "model: \"\"", 1),
		"missing image": strings.Replace(validSuiteYAML, "runtimeImage: reference:dev", "runtimeImage: \"\"", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSuite([]byte(input), ""); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestParseSuiteRejectsCasesWithoutGraders(t *testing.T) {
	input := strings.Replace(
		validSuiteYAML,
		"      graders:\n        - type: terminalPhase\n          phase: Succeeded",
		"      graders: []",
		1,
	)
	if _, err := ParseSuite([]byte(input), ""); err == nil ||
		!strings.Contains(err.Error(), "at least one deterministic grader") {
		t.Fatalf("expected missing-grader validation error, got %v", err)
	}
}

func TestValidateGraderRejectsVacuousExpectations(t *testing.T) {
	empty := ""
	for name, grader := range map[string]Grader{
		"structured output": {
			Type: GraderStructuredOutput, StructuredOutput: &StructuredOutputExpectation{},
		},
		"contains empty": {
			Type: GraderStatusResult, StatusResult: &StringMatch{Contains: &empty},
		},
		"not contains empty": {
			Type: GraderStatusResult, StatusResult: &StringMatch{NotContains: &empty},
		},
		"empty error code": {
			Type: GraderEnvelopeError,
		},
		"duplicate usage field": {
			Type: GraderUsageFields, UsageFields: []string{"tokens", "tokens"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateGrader(grader); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateStructuredOutputGraderBranches(t *testing.T) {
	present := true
	valid := true
	tests := map[string]struct {
		expectation *StructuredOutputExpectation
		wantError   string
	}{
		"missing expectation": {
			wantError: "structuredOutput expectation is required",
		},
		"empty expectation": {
			expectation: &StructuredOutputExpectation{},
			wantError:   "structuredOutput requires present, valid, or mediaType",
		},
		"blank media type only": {
			expectation: &StructuredOutputExpectation{MediaType: " \t "},
			wantError:   "structuredOutput.mediaType must not be blank",
		},
		"blank media type with present": {
			expectation: &StructuredOutputExpectation{Present: &present, MediaType: " \t "},
			wantError:   "structuredOutput.mediaType must not be blank",
		},
		"present only": {
			expectation: &StructuredOutputExpectation{Present: &present},
		},
		"valid only": {
			expectation: &StructuredOutputExpectation{Valid: &valid},
		},
		"media type only": {
			expectation: &StructuredOutputExpectation{MediaType: "application/json"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateStructuredOutputGrader(Grader{
				Type:             GraderStructuredOutput,
				StructuredOutput: test.expectation,
			})
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("valid expectation was rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("validation error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestNameForCaseIsStableDNSSafeAndBounded(t *testing.T) {
	first := NameForCase("Suite_Name", "CASE with spaces", strings.Repeat("x", 100))
	second := NameForCase("Suite_Name", "CASE with spaces", strings.Repeat("x", 100))
	if first != second || len(first) > 63 {
		t.Fatalf("unexpected name %q / %q", first, second)
	}
	for _, character := range first {
		if !(character >= 'a' && character <= 'z') &&
			!(character >= '0' && character <= '9') &&
			character != '-' {
			t.Fatalf("name contains invalid character %q", character)
		}
	}
	other := NameForCase("Suite_Name", strings.Repeat("c", 100)+"a", strings.Repeat("x", 100))
	another := NameForCase("Suite_Name", strings.Repeat("c", 100)+"b", strings.Repeat("x", 100))
	if other == another {
		t.Fatalf("long case names collided: %q", other)
	}
	sanitizedA := NameForCase("suite", "case_with_separator", "invocation")
	sanitizedB := NameForCase("suite", "case-with-separator", "invocation")
	if sanitizedA == sanitizedB {
		t.Fatalf("raw case IDs that sanitize alike collided: %q", sanitizedA)
	}
	if NameForCase("suite", "case", "invocation") == "suite-case-invocation" {
		t.Fatal("short names must also carry a collision-resistant hash")
	}
}
