package status

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TerminationPayload is the JSON written to /dev/termination-log by runtime images.
type TerminationPayload struct {
	Result      string  `json:"result"`
	TokensUsed  int32   `json:"tokensUsed"`
	DollarsUsed float64 `json:"dollarsUsed"`
	Error       string  `json:"error"`
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
	if message == "" {
		return TerminationPayload{}, nil
	}

	var payload TerminationPayload
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		if strings.HasPrefix(message, "{") {
			return TerminationPayload{Result: message}, fmt.Errorf("decode termination payload: %w", err)
		}
		return TerminationPayload{Result: message}, nil
	}
	return payload, nil
}
