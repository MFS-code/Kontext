package engine

import (
	"encoding/json"
	"time"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
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

func Success(response runtimeapi.CompletionResponse, metadata Metadata) resultv1alpha1.Envelope {
	text := runtimeapi.MessageText(response.Message)
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
		Usage: envelopeUsage(response.Usage),
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

func Failure(
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

func envelopeUsage(providerUsage runtimeapi.Usage) *resultv1alpha1.Usage {
	if providerUsage.InputTokens == nil &&
		providerUsage.OutputTokens == nil &&
		providerUsage.TotalTokens == nil {
		return nil
	}
	return &resultv1alpha1.Usage{
		InputTokens:  providerUsage.InputTokens,
		OutputTokens: providerUsage.OutputTokens,
		TotalTokens:  providerUsage.TotalTokens,
	}
}
