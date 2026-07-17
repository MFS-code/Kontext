package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
)

const (
	reporterFailureExitCode  = 125
	childStartExitCode       = 126
	truncatedOutputMediaType = "application/vnd.kontext.truncated+json"
	truncatedOutputJSON      = `{"truncated":true}`
)

func runReporter(
	ctx context.Context,
	config Config,
	stdout io.Writer,
	stderr io.Writer,
	signals <-chan os.Signal,
) int {
	capture := newCapture(config.Format, config.MaxCaptureBytes)
	child, err := runChild(ctx, config.Command, stdout, stderr, capture, signals)
	if err != nil {
		exitCode := reporterFailureExitCode
		errorCode := "reporter_internal"
		var startErr *ChildStartError
		if errors.As(err, &startErr) {
			exitCode = childStartExitCode
			errorCode = "child_start_failed"
		}
		fmt.Fprintf(stderr, "kontext reporter: %v\n", err)
		if writeErr := writeEnvelope(config.TerminationPath, failureEnvelope(errorCode, err.Error())); writeErr != nil {
			fmt.Fprintf(stderr, "kontext reporter: write failure result: %v\n", writeErr)
		}
		return exitCode
	}

	envelope := envelopeFromCapture(config.Format, capture.Result(), child.ExitCode)
	if err := writeEnvelope(config.TerminationPath, envelope); err != nil {
		fmt.Fprintf(stderr, "kontext reporter: write termination message: %v\n", err)
		return reporterFailureExitCode
	}
	return child.ExitCode
}

func envelopeFromCapture(format CaptureFormat, captured CapturedResult, childExitCode int) resultv1alpha1.Envelope {
	var envelope resultv1alpha1.Envelope
	switch format {
	case CaptureFormatLastLine:
		envelope = resultv1alpha1.Envelope{
			APIVersion: resultv1alpha1.APIVersion,
			Outcome:    resultv1alpha1.OutcomeSucceeded,
		}
		if captured.Truncated {
			envelope.Output = &resultv1alpha1.Output{
				MediaType: truncatedOutputMediaType,
				Value:     json.RawMessage(truncatedOutputJSON),
			}
			envelope.Truncation = &resultv1alpha1.Truncation{
				OriginalBytes:   captured.OriginalBytes,
				OutputTruncated: true,
			}
		} else if captured.Found {
			value, _ := json.Marshal(string(captured.Data))
			envelope.Output = &resultv1alpha1.Output{
				MediaType: resultv1alpha1.DefaultMediaType,
				Value:     value,
			}
		}
	case CaptureFormatKontextEnvelope:
		envelope = envelopeFromPrefixedCandidate(captured)
	default:
		envelope = failureEnvelope("result_format_invalid", fmt.Sprintf("unsupported result format %q", format))
	}

	if childExitCode != 0 && envelope.Outcome != resultv1alpha1.OutcomeFailed {
		envelope.Outcome = resultv1alpha1.OutcomeFailed
		envelope.Error = &resultv1alpha1.ErrorInfo{
			Code:    "agent_process_exit",
			Message: fmt.Sprintf("agent process exited with code %d", childExitCode),
		}
	}
	return envelope
}

func envelopeFromPrefixedCandidate(captured CapturedResult) resultv1alpha1.Envelope {
	if !captured.Found {
		return failureEnvelope("result_missing", "agent did not emit a prefixed Kontext result")
	}
	if captured.Truncated {
		return failureEnvelope("result_invalid", "prefixed Kontext result exceeded the capture limit")
	}

	parsed, err := resultv1alpha1.Parse(string(captured.Data))
	if err != nil {
		return failureEnvelope("result_invalid", fmt.Sprintf("invalid prefixed Kontext result: %v", err))
	}
	if parsed.Envelope == nil {
		return failureEnvelope("result_invalid", "prefixed Kontext result must be a versioned envelope")
	}
	return *parsed.Envelope
}

func failureEnvelope(code string, message string) resultv1alpha1.Envelope {
	return resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeFailed,
		Error: &resultv1alpha1.ErrorInfo{
			Code:    code,
			Message: message,
		},
	}
}

func writeEnvelope(path string, envelope resultv1alpha1.Envelope) error {
	payload, err := resultv1alpha1.Compact(envelope, resultv1alpha1.MaxTerminationMessageBytes)
	if err != nil {
		return fmt.Errorf("compact result envelope: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
