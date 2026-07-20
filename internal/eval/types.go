package eval

import (
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

const (
	APIVersion = "kontext.dev/eval/v1alpha1"
	Kind       = "EvalSuite"
	RecordKind = "EvalRecord"
)

type Duration struct {
	time.Duration
}

func (duration *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	duration.Duration = parsed
	return nil
}

func (duration Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(duration.String())
}

type EvalSuite struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	Metadata   Metadata  `json:"metadata"`
	Spec       SuiteSpec `json:"spec"`
}

type Metadata struct {
	Name string `json:"name"`
}

type SuiteSpec struct {
	Defaults   SuiteDefaults    `json:"defaults"`
	Assertions []SuiteAssertion `json:"assertions,omitempty"`
	Cases      []Case           `json:"cases"`
}

type SuiteDefaults struct {
	Namespace    string    `json:"namespace,omitempty"`
	Timeout      *Duration `json:"timeout,omitempty"`
	RuntimeImage string    `json:"runtimeImage,omitempty"`
}

type Case struct {
	ID          string                       `json:"id"`
	Description string                       `json:"description,omitempty"`
	AgentRun    kontextv1alpha1.AgentRunSpec `json:"agentRun"`
	Timeout     *Duration                    `json:"timeout,omitempty"`
	Graders     []Grader                     `json:"graders"`
}

type SuiteAssertionType string

const (
	SuiteAssertionFieldsEqual      SuiteAssertionType = "fieldsEqual"
	SuiteAssertionForbiddenMarkers SuiteAssertionType = "forbiddenMarkers"
)

type SuiteAssertion struct {
	Type    SuiteAssertionType `json:"type"`
	Records []string           `json:"records,omitempty"`
	Fields  []string           `json:"fields"`
	Markers []string           `json:"markers,omitempty"`
}

type SuiteAssertionResult struct {
	Type    SuiteAssertionType `json:"type"`
	Records []string           `json:"records,omitempty"`
	Fields  []string           `json:"fields"`
	Pass    bool               `json:"pass"`
	Message string             `json:"message"`
}

type GraderType string

const (
	GraderTerminalPhase    GraderType = "terminalPhase"
	GraderStatusResult     GraderType = "statusResult"
	GraderStructuredOutput GraderType = "structuredOutput"
	GraderUsageFields      GraderType = "usageFields"
	GraderEnvelopeError    GraderType = "envelopeErrorCode"
	GraderEnvelopeOutcome  GraderType = "envelopeOutcome"
	GraderExecutionModel   GraderType = "executionModel"
	GraderEnvelopeTurns    GraderType = "envelopeTurns"
	GraderEnvelopeTools    GraderType = "envelopeToolCalls"
	GraderEventCount       GraderType = "eventCount"
	GraderToolEvents       GraderType = "toolEvents"
	GraderDuration         GraderType = "duration"
	GraderPodExitCode      GraderType = "podExitCode"
)

type StringMatch struct {
	Exact       *string `json:"exact,omitempty"`
	Contains    *string `json:"contains,omitempty"`
	NotContains *string `json:"notContains,omitempty"`
}

type StructuredOutputExpectation struct {
	MediaType string `json:"mediaType,omitempty"`
	Present   *bool  `json:"present,omitempty"`
	Valid     *bool  `json:"valid,omitempty"`
}

type EventCountExpectation struct {
	Type  eventv1alpha1.Type `json:"type"`
	Count int                `json:"count"`
}

type ToolExpectation struct {
	Name      string `json:"name"`
	Count     int    `json:"count"`
	IsError   *bool  `json:"isError,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	Truncated *bool  `json:"truncated,omitempty"`
}

type Grader struct {
	Type             GraderType                    `json:"type"`
	Phase            kontextv1alpha1.AgentRunPhase `json:"phase,omitempty"`
	StatusResult     *StringMatch                  `json:"statusResult,omitempty"`
	StructuredOutput *StructuredOutputExpectation  `json:"structuredOutput,omitempty"`
	UsageFields      []string                      `json:"usageFields,omitempty"`
	ErrorCode        string                        `json:"errorCode,omitempty"`
	Outcome          resultv1alpha1.Outcome        `json:"outcome,omitempty"`
	Model            string                        `json:"model,omitempty"`
	Turns            *int32                        `json:"turns,omitempty"`
	ToolCalls        *int32                        `json:"toolCalls,omitempty"`
	Event            *EventCountExpectation        `json:"event,omitempty"`
	Tool             *ToolExpectation              `json:"tool,omitempty"`
	MaxDuration      Duration                      `json:"maxDuration,omitempty"`
	ExitCode         *int32                        `json:"exitCode,omitempty"`
}

type RunRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	PodName   string `json:"podName,omitempty"`
}

type StatusOutput struct {
	MediaType string          `json:"mediaType"`
	Value     json.RawMessage `json:"value"`
}

type EventSummary struct {
	Counts    map[eventv1alpha1.Type]int `json:"counts,omitempty"`
	Tools     []ToolEvent                `json:"tools,omitempty"`
	Lifecycle []string                   `json:"lifecycle,omitempty"`
	Errors    []string                   `json:"errors,omitempty"`
	Metadata  []EventMetadata            `json:"metadata,omitempty"`
	Truncated bool                       `json:"truncated,omitempty"`
}

type ToolEvent struct {
	Name      string `json:"name,omitempty"`
	Count     int32  `json:"count,omitempty"`
	IsError   bool   `json:"isError,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type EventMetadata struct {
	Timestamp time.Time          `json:"timestamp"`
	Type      eventv1alpha1.Type `json:"type"`
	Phase     string             `json:"phase,omitempty"`
	Name      string             `json:"name,omitempty"`
	ErrorCode string             `json:"errorCode,omitempty"`
	IsError   bool               `json:"isError,omitempty"`
	Truncated bool               `json:"truncated,omitempty"`
}

type Grade struct {
	Type     GraderType `json:"type"`
	Pass     bool       `json:"pass"`
	Expected any        `json:"expected,omitempty"`
	Observed any        `json:"observed,omitempty"`
	Message  string     `json:"message,omitempty"`
}

type JudgeResult struct {
	Configured bool    `json:"configured"`
	Pass       bool    `json:"pass"`
	Score      float64 `json:"score,omitempty"`
	Rationale  string  `json:"rationale,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type EnvelopeObservation struct {
	Outcome   resultv1alpha1.Outcome        `json:"outcome,omitempty"`
	Error     *EnvelopeErrorObservation     `json:"error,omitempty"`
	Execution *EnvelopeExecutionObservation `json:"execution,omitempty"`
}

type EnvelopeErrorObservation struct {
	Code string `json:"code"`
}

type EnvelopeExecutionObservation struct {
	Model     string `json:"model,omitempty"`
	Turns     *int32 `json:"turns,omitempty"`
	ToolCalls *int32 `json:"toolCalls,omitempty"`
}

type Record struct {
	APIVersion       string                        `json:"apiVersion"`
	Kind             string                        `json:"kind"`
	Suite            string                        `json:"suite"`
	CaseID           string                        `json:"caseId"`
	Description      string                        `json:"description,omitempty"`
	Run              RunRef                        `json:"run"`
	StartedAt        time.Time                     `json:"startedAt"`
	CompletedAt      time.Time                     `json:"completedAt"`
	DurationMillis   int64                         `json:"durationMillis"`
	TerminalPhase    kontextv1alpha1.AgentRunPhase `json:"terminalPhase,omitempty"`
	StatusResult     string                        `json:"statusResult,omitempty"`
	StatusOutput     *StatusOutput                 `json:"statusOutput,omitempty"`
	StatusUsage      *kontextv1alpha1.UsageStatus  `json:"statusUsage,omitempty"`
	PodExitCode      *int32                        `json:"podExitCode,omitempty"`
	Envelope         *EnvelopeObservation          `json:"envelope,omitempty"`
	Events           EventSummary                  `json:"events"`
	Grades           []Grade                       `json:"grades"`
	Judge            *JudgeResult                  `json:"judge,omitempty"`
	CollectionErrors []string                      `json:"collectionErrors,omitempty"`
	Pass             bool                          `json:"pass"`
}

type Summary struct {
	APIVersion        string                 `json:"apiVersion"`
	Suite             string                 `json:"suite"`
	StartedAt         time.Time              `json:"startedAt"`
	CompletedAt       time.Time              `json:"completedAt"`
	Total             int                    `json:"total"`
	Passed            int                    `json:"passed"`
	Failed            int                    `json:"failed"`
	Assertions        []SuiteAssertionResult `json:"assertions,omitempty"`
	AssertionFailures int                    `json:"assertionFailures"`
	Pass              bool                   `json:"pass"`
	RecordPath        string                 `json:"recordPath"`
}

func podExitCode(pod *corev1.Pod) *int32 {
	if pod == nil {
		return nil
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "runtime" && status.State.Terminated != nil {
			value := status.State.Terminated.ExitCode
			return &value
		}
	}
	return nil
}

func newAgentRun(name, namespace string, spec kontextv1alpha1.AgentRunSpec, labels map[string]string) *kontextv1alpha1.AgentRun {
	return &kontextv1alpha1.AgentRun{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kontextv1alpha1.GroupVersion.String(),
			Kind:       "AgentRun",
		},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec:       spec,
	}
}
