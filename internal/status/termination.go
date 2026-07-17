package status

import (
	"fmt"
	"strings"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
)

// TerminationPayload is the normalized terminal summary reported by a runtime.
type TerminationPayload struct {
	Result   string
	Output   *resultv1alpha1.Output
	Usage    *resultv1alpha1.Usage
	Error    string
	Outcome  resultv1alpha1.Outcome
	Envelope *resultv1alpha1.Envelope
	Legacy   bool
}

// ParseTerminationMessage decodes a Pod termination message into a payload.
//
// The runtime contract (SPEC.md) is that agents write a JSON object to
// /dev/termination-log. A message that is not JSON is treated as a plain-text
// result for convenience. A message that looks like JSON (starts with '{') but
// fails to decode is a malformed payload: the raw text is still returned as the
// result so nothing is lost, but a non-nil error is reported so callers can
// surface the failure instead of silently accepting a broken payload.
func ParseTerminationMessage(message string) (TerminationPayload, error) {
	message = strings.TrimSpace(message)
	parsed, err := resultv1alpha1.Parse(message)
	if err != nil {
		return TerminationPayload{Result: message}, fmt.Errorf("parse termination payload: %w", err)
	}
	payload := TerminationPayload{
		Result:   resultv1alpha1.ProjectLegacyResult(parsed.Output),
		Output:   parsed.Output,
		Usage:    parsed.Usage,
		Outcome:  parsed.Outcome,
		Envelope: parsed.Envelope,
		Legacy:   parsed.Legacy,
	}
	if parsed.Error != nil {
		payload.Error = parsed.Error.Message
	}
	return payload, nil
}
