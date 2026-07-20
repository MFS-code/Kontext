package engine

import (
	"encoding/json"
	"io"
	"time"

	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/events"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

type Metadata struct {
	Provider    string
	Model       string
	RequestID   string
	StartedAt   time.Time
	CompletedAt time.Time
	Turns       int32
	ToolCalls   int32
}

func Success(
	message runtimeapi.Message,
	usage runtimeapi.Usage,
	metadata Metadata,
) resultv1alpha1.Envelope {
	text := runtimeapi.MessageText(message)
	value, _ := json.Marshal(text)
	turns := metadata.Turns
	toolCalls := metadata.ToolCalls
	durationMillis := metadata.CompletedAt.Sub(metadata.StartedAt).Milliseconds()

	return resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeSucceeded,
		Output: &resultv1alpha1.Output{
			MediaType: resultv1alpha1.DefaultMediaType,
			Value:     value,
		},
		Usage: envelopeUsage(usage),
		Timing: &resultv1alpha1.Timing{
			StartedAt:      &metadata.StartedAt,
			CompletedAt:    &metadata.CompletedAt,
			DurationMillis: &durationMillis,
		},
		Execution: &resultv1alpha1.Execution{
			Provider:  metadata.Provider,
			Model:     metadata.Model,
			RequestID: metadata.RequestID,
			Turns:     &turns,
			ToolCalls: &toolCalls,
		},
	}
}

func failureEnvelope(
	code string,
	message string,
	retryable *bool,
	metadata Metadata,
) resultv1alpha1.Envelope {
	durationMillis := metadata.CompletedAt.Sub(metadata.StartedAt).Milliseconds()
	turns := metadata.Turns
	toolCalls := metadata.ToolCalls
	return resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeFailed,
		Timing: &resultv1alpha1.Timing{
			StartedAt:      &metadata.StartedAt,
			CompletedAt:    &metadata.CompletedAt,
			DurationMillis: &durationMillis,
		},
		Execution: &resultv1alpha1.Execution{
			Provider:  metadata.Provider,
			Model:     metadata.Model,
			RequestID: metadata.RequestID,
			Turns:     &turns,
			ToolCalls: &toolCalls,
		},
		Error: &resultv1alpha1.ErrorInfo{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	}
}

// EmitFailure emits the error event and terminal envelope used when execution
// cannot reach Runner, such as invalid environment configuration.
func EmitFailure(
	writer io.Writer,
	emitter Emitter,
	code string,
	message string,
	retryable *bool,
	metadata Metadata,
) error {
	if emitter != nil {
		emitter.Emit(events.TypeError, map[string]any{
			"code":      code,
			"message":   message,
			"retryable": retryable,
		})
	}
	return resultv1alpha1.WriteEnvelopeLine(
		writer,
		failureEnvelope(code, message, retryable, metadata),
	)
}

func envelopeUsage(providerUsage runtimeapi.Usage) *resultv1alpha1.Usage {
	if providerUsage.InputTokens == nil &&
		providerUsage.OutputTokens == nil &&
		providerUsage.TotalTokens == nil &&
		providerUsage.ReasoningTokens == nil {
		return nil
	}
	return &resultv1alpha1.Usage{
		InputTokens:     providerUsage.InputTokens,
		OutputTokens:    providerUsage.OutputTokens,
		TotalTokens:     providerUsage.TotalTokens,
		ReasoningTokens: providerUsage.ReasoningTokens,
	}
}
