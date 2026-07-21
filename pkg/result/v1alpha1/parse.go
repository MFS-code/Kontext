package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
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

// ErrVersionedEnvelopeRequired indicates that a termination message was empty
// or used a legacy wire format where a versioned envelope was required.
var ErrVersionedEnvelopeRequired = errors.New("versioned result envelope required")

// Parse decodes a termination message into the current result envelope.
// The returned boolean reports whether the message used a legacy JSON or
// plain-text wire format.
func Parse(message string) (Envelope, bool, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return successfulEnvelope(), false, nil
	}
	if !strings.HasPrefix(message, "{") {
		envelope := successfulEnvelope()
		envelope.Output = outputFromText(message)
		return envelope, true, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &fields); err != nil {
		return Envelope{}, false, fmt.Errorf("decode termination payload: %w", err)
	}
	if _, versioned := fields["apiVersion"]; versioned {
		var envelope Envelope
		if err := json.Unmarshal([]byte(message), &envelope); err != nil {
			return Envelope{}, false, fmt.Errorf("decode result envelope: %w", err)
		}
		if err := envelope.Validate(); err != nil {
			return Envelope{}, false, fmt.Errorf("validate result envelope: %w", err)
		}
		return envelope, false, nil
	}
	if !hasLegacyField(fields) {
		return Envelope{}, false, errors.New("decode termination payload: unrecognized JSON object")
	}

	var legacy LegacyPayload
	if err := json.Unmarshal([]byte(message), &legacy); err != nil {
		return Envelope{}, false, fmt.Errorf("decode legacy termination payload: %w", err)
	}
	envelope := successfulEnvelope()
	envelope.Output = outputFromText(legacy.Result)
	envelope.Usage = usageFromLegacy(legacy)
	if legacy.Error != "" {
		envelope.Error = &ErrorInfo{Message: legacy.Error}
	}
	return envelope, true, nil
}

func hasLegacyField(fields map[string]json.RawMessage) bool {
	for _, key := range [...]string{"result", "tokensUsed", "dollarsUsed", "error"} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

// ParseVersioned decodes a termination message only when the wire payload
// contains a versioned result envelope.
func ParseVersioned(message string) (Envelope, error) {
	envelope, legacy, err := Parse(message)
	if err != nil {
		return Envelope{}, err
	}
	if legacy || strings.TrimSpace(message) == "" {
		return Envelope{}, ErrVersionedEnvelopeRequired
	}
	return envelope, nil
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

func successfulEnvelope() Envelope {
	return Envelope{
		APIVersion: APIVersion,
		Outcome:    OutcomeSucceeded,
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
