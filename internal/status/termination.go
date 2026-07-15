package status

import (
	"encoding/json"
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
func ParseTerminationMessage(message string) TerminationPayload {
	message = strings.TrimSpace(message)
	if message == "" {
		return TerminationPayload{}
	}

	var payload TerminationPayload
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		return TerminationPayload{Result: message}
	}
	return payload
}
