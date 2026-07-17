package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// LegacyPayload is the pre-versioned Kontext termination contract.
type LegacyPayload struct {
	Result      string   `json:"result"`
	TokensUsed  *int64   `json:"tokensUsed,omitempty"`
	DollarsUsed *float64 `json:"dollarsUsed,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// ParsedResult is the normalized representation of a termination message.
type ParsedResult struct {
	Envelope *Envelope
	Output   *Output
	Usage    *Usage
	Error    *ErrorInfo
	Outcome  Outcome
	Legacy   bool
}

// Parse decodes a versioned envelope, legacy payload, or plain-text result.
func Parse(message string) (ParsedResult, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return ParsedResult{}, nil
	}
	if !strings.HasPrefix(message, "{") {
		return parsedPlainText(message), nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &fields); err != nil {
		return ParsedResult{}, fmt.Errorf("decode termination payload: %w", err)
	}
	if _, versioned := fields["apiVersion"]; versioned {
		var envelope Envelope
		if err := json.Unmarshal([]byte(message), &envelope); err != nil {
			return ParsedResult{}, fmt.Errorf("decode result envelope: %w", err)
		}
		if err := envelope.Validate(); err != nil {
			return ParsedResult{}, fmt.Errorf("validate result envelope: %w", err)
		}
		return ParsedResult{
			Envelope: &envelope,
			Output:   envelope.Output,
			Usage:    envelope.Usage,
			Error:    envelope.Error,
			Outcome:  envelope.Outcome,
		}, nil
	}

	var legacy LegacyPayload
	if err := json.Unmarshal([]byte(message), &legacy); err != nil {
		return ParsedResult{}, fmt.Errorf("decode legacy termination payload: %w", err)
	}
	parsed := ParsedResult{
		Output:  outputFromText(legacy.Result),
		Usage:   usageFromLegacy(legacy),
		Outcome: OutcomeSucceeded,
		Legacy:  true,
	}
	if legacy.Error != "" {
		parsed.Error = &ErrorInfo{Message: legacy.Error}
	}
	return parsed, nil
}

// ProjectLegacyResult deterministically projects structured output into the
// backward-compatible AgentRun status.result string.
func ProjectLegacyResult(output *Output) string {
	if output == nil || len(bytes.TrimSpace(output.Value)) == 0 {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(output.MediaType), "text/") {
		var text string
		if err := json.Unmarshal(output.Value, &text); err == nil {
			return text
		}
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, output.Value); err != nil {
		return ""
	}
	return compact.String()
}

func parsedPlainText(message string) ParsedResult {
	return ParsedResult{
		Output:  outputFromText(message),
		Outcome: OutcomeSucceeded,
		Legacy:  true,
	}
}

func outputFromText(value string) *Output {
	if value == "" {
		return nil
	}
	encoded, _ := json.Marshal(value)
	return &Output{
		MediaType: DefaultMediaType,
		Value:     encoded,
	}
}

func usageFromLegacy(payload LegacyPayload) *Usage {
	if payload.TokensUsed == nil && payload.DollarsUsed == nil {
		return nil
	}
	return &Usage{
		TotalTokens: payload.TokensUsed,
		Dollars:     payload.DollarsUsed,
	}
}
