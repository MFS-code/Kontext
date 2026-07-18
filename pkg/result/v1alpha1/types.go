// Package v1alpha1 defines the versioned result contract shared by Kontext
// runtimes, reporters, and the control plane.
package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	APIVersion                         = "kontext.dev/result/v1alpha1"
	DefaultMediaType                   = "text/plain"
	MaxTerminationMessageBytes         = 4096
	OutcomeSucceeded           Outcome = "Succeeded"
	OutcomeFailed              Outcome = "Failed"
)

// Outcome is the runtime-reported terminal outcome.
type Outcome string

// Envelope is the compact terminal result written to /dev/termination-log.
// Full execution events belong in the runtime's JSONL output stream.
type Envelope struct {
	APIVersion string                     `json:"apiVersion"`
	Outcome    Outcome                    `json:"outcome"`
	Output     *Output                    `json:"output,omitempty"`
	Usage      *Usage                     `json:"usage,omitempty"`
	Timing     *Timing                    `json:"timing,omitempty"`
	Execution  *Execution                 `json:"execution,omitempty"`
	Error      *ErrorInfo                 `json:"error,omitempty"`
	Artifacts  []Artifact                 `json:"artifacts,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
	Truncation *Truncation                `json:"truncation,omitempty"`
}

// Output carries an arbitrary JSON value and describes how consumers should
// interpret it.
type Output struct {
	MediaType string          `json:"mediaType"`
	Value     json.RawMessage `json:"value"`
}

// Usage records only metrics actually measured by a runtime or provider.
// Pointer fields distinguish a measured zero from a missing measurement.
type Usage struct {
	InputTokens     *int64   `json:"inputTokens,omitempty"`
	OutputTokens    *int64   `json:"outputTokens,omitempty"`
	TotalTokens     *int64   `json:"totalTokens,omitempty"`
	ReasoningTokens *int64   `json:"reasoningTokens,omitempty"`
	Dollars         *float64 `json:"dollars,omitempty"`
}

// Timing records provider/runtime timing when it is available.
type Timing struct {
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
	DurationMillis    *int64     `json:"durationMillis,omitempty"`
	ProviderLatencyMS *int64     `json:"providerLatencyMillis,omitempty"`
}

// Execution contains compact, non-secret metadata about the execution.
type Execution struct {
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Turns     *int32 `json:"turns,omitempty"`
	ToolCalls *int32 `json:"toolCalls,omitempty"`
}

// ErrorInfo is a safe, user-facing terminal error summary.
type ErrorInfo struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Retryable *bool  `json:"retryable,omitempty"`
}

// Artifact points to result data stored outside Kubernetes status.
type Artifact struct {
	Name      string `json:"name"`
	URI       string `json:"uri"`
	MediaType string `json:"mediaType,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

// Truncation states which fields were removed to fit a bounded transport.
type Truncation struct {
	OriginalBytes       int  `json:"originalBytes"`
	OutputTruncated     bool `json:"outputTruncated,omitempty"`
	ArtifactsTruncated  bool `json:"artifactsTruncated,omitempty"`
	ExtensionsTruncated bool `json:"extensionsTruncated,omitempty"`
}

// Validate rejects envelopes that cannot be consumed deterministically.
func (e Envelope) Validate() error {
	if e.APIVersion != APIVersion {
		return fmt.Errorf("unsupported result apiVersion %q", e.APIVersion)
	}
	switch e.Outcome {
	case OutcomeSucceeded, OutcomeFailed:
	default:
		return fmt.Errorf("unsupported result outcome %q", e.Outcome)
	}
	if e.Output != nil {
		if strings.TrimSpace(e.Output.MediaType) == "" {
			return errors.New("result output mediaType is required")
		}
		if len(bytes.TrimSpace(e.Output.Value)) == 0 || !json.Valid(e.Output.Value) {
			return errors.New("result output value must be valid JSON")
		}
	}
	if e.Error != nil && strings.TrimSpace(e.Error.Message) == "" {
		return errors.New("result error message is required")
	}
	if e.Outcome == OutcomeFailed && e.Error == nil {
		return errors.New("failed result requires error details")
	}
	if e.Outcome == OutcomeSucceeded && e.Error != nil {
		return errors.New("succeeded result cannot contain terminal error details")
	}
	if e.Usage != nil {
		if err := validateUsage(*e.Usage); err != nil {
			return err
		}
	}
	if e.Timing != nil {
		if e.Timing.DurationMillis != nil && *e.Timing.DurationMillis < 0 {
			return errors.New("result timing durationMillis cannot be negative")
		}
		if e.Timing.ProviderLatencyMS != nil && *e.Timing.ProviderLatencyMS < 0 {
			return errors.New("result timing providerLatencyMillis cannot be negative")
		}
		if e.Timing.StartedAt != nil && e.Timing.CompletedAt != nil && e.Timing.CompletedAt.Before(*e.Timing.StartedAt) {
			return errors.New("result timing completedAt cannot precede startedAt")
		}
	}
	if e.Execution != nil {
		if e.Execution.Turns != nil && *e.Execution.Turns < 0 {
			return errors.New("result execution turns cannot be negative")
		}
		if e.Execution.ToolCalls != nil && *e.Execution.ToolCalls < 0 {
			return errors.New("result execution toolCalls cannot be negative")
		}
	}
	for index, artifact := range e.Artifacts {
		if strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.URI) == "" {
			return fmt.Errorf("result artifact %d requires name and uri", index)
		}
	}
	for name, value := range e.Extensions {
		if !strings.Contains(name, "/") {
			return fmt.Errorf("result extension %q must use a namespaced key", name)
		}
		if !json.Valid(value) {
			return fmt.Errorf("result extension %q must contain valid JSON", name)
		}
	}
	return nil
}

func validateUsage(usage Usage) error {
	metrics := map[string]*int64{
		"inputTokens":     usage.InputTokens,
		"outputTokens":    usage.OutputTokens,
		"totalTokens":     usage.TotalTokens,
		"reasoningTokens": usage.ReasoningTokens,
	}
	for name, value := range metrics {
		if value != nil && *value < 0 {
			return fmt.Errorf("result usage %s cannot be negative", name)
		}
	}
	if usage.OutputTokens != nil &&
		usage.ReasoningTokens != nil &&
		*usage.ReasoningTokens > *usage.OutputTokens {
		return fmt.Errorf(
			"result usage reasoningTokens %d exceeds outputTokens %d",
			*usage.ReasoningTokens,
			*usage.OutputTokens,
		)
	}
	if usage.Dollars != nil && *usage.Dollars < 0 {
		return errors.New("result usage dollars cannot be negative")
	}
	return nil
}
