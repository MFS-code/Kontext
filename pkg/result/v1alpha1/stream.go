package v1alpha1

import (
	"bytes"
	"fmt"
	"io"
)

// EnvelopeLinePrefix identifies a complete versioned result envelope in a
// runtime's stdout stream.
const EnvelopeLinePrefix = "KONTEXT_RESULT:"

// WriteEnvelopeLine compacts the envelope and writes the single stdout line
// that reporters recognize. This is the producer half of the stream contract;
// ExtractEnvelopePayload is the consumer half.
func WriteEnvelopeLine(writer io.Writer, envelope Envelope) error {
	payload, err := Compact(envelope, MaxTerminationMessageBytes)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "%s %s\n", EnvelopeLinePrefix, payload)
	return err
}

// ExtractEnvelopePayload reports whether line carries a result envelope and,
// if so, returns the payload with the prefix and surrounding whitespace
// stripped. The returned slice aliases line.
func ExtractEnvelopePayload(line []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte(EnvelopeLinePrefix)) {
		return nil, false
	}
	return bytes.TrimSpace(trimmed[len(EnvelopeLinePrefix):]), true
}
