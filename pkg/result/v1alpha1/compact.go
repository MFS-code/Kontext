package v1alpha1

import (
	"encoding/json"
	"fmt"
)

const truncatedOutputJSON = `{"truncated":true}`

// Compact serializes an envelope within maxBytes. It removes the least
// status-relevant data first and always marks data loss explicitly.
func Compact(envelope Envelope, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("result envelope byte limit must be positive")
	}
	if err := envelope.Validate(); err != nil {
		return nil, err
	}

	original, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal result envelope: %w", err)
	}
	if len(original) <= maxBytes {
		return original, nil
	}

	compacted, err := cloneEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	compacted.Truncation = &Truncation{OriginalBytes: len(original)}

	if len(compacted.Extensions) > 0 {
		compacted.Extensions = nil
		compacted.Truncation.ExtensionsTruncated = true
		if encoded, ok := marshalWithin(compacted, maxBytes); ok {
			return encoded, nil
		}
	}
	if len(compacted.Artifacts) > 0 {
		compacted.Artifacts = nil
		compacted.Truncation.ArtifactsTruncated = true
		if encoded, ok := marshalWithin(compacted, maxBytes); ok {
			return encoded, nil
		}
	}
	if compacted.Output != nil {
		compacted.Output = &Output{
			MediaType: "application/vnd.kontext.truncated+json",
			Value:     json.RawMessage(truncatedOutputJSON),
		}
		compacted.Truncation.OutputTruncated = true
		if encoded, ok := marshalWithin(compacted, maxBytes); ok {
			return encoded, nil
		}
	}

	// Large diagnostic and execution strings can still exceed the transport
	// after output data is removed.
	if compacted.Error != nil {
		compacted.Error.Code = ""
		compacted.Error.Message = "result error details truncated"
	}
	compacted.Execution = nil
	compacted.Timing = nil
	if encoded, ok := marshalWithin(compacted, maxBytes); ok {
		return encoded, nil
	}

	minimal := Envelope{
		APIVersion: APIVersion,
		Outcome:    compacted.Outcome,
		Truncation: compacted.Truncation,
	}
	if minimal.Outcome == OutcomeFailed {
		minimal.Error = &ErrorInfo{Message: "result error details truncated"}
	}
	if encoded, ok := marshalWithin(minimal, maxBytes); ok {
		return encoded, nil
	}
	return nil, fmt.Errorf("result envelope cannot fit within %d bytes", maxBytes)
}

func cloneEnvelope(envelope Envelope) (Envelope, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal result envelope for compaction: %w", err)
	}
	var clone Envelope
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return Envelope{}, fmt.Errorf("clone result envelope for compaction: %w", err)
	}
	return clone, nil
}

func marshalWithin(envelope Envelope, maxBytes int) ([]byte, bool) {
	encoded, err := json.Marshal(envelope)
	return encoded, err == nil && len(encoded) <= maxBytes
}
