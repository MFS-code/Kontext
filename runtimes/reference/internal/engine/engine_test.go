package engine_test

import (
	"context"
	"testing"
	"time"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/engine"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/events"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
)

func TestRunnerCompletesFakeConversation(t *testing.T) {
	emitter := &recordingEmitter{}
	runner := engine.Runner{Emitter: emitter}
	result := runner.Run(context.Background(), baseConfig())

	if result.ExitCode != 0 || result.Envelope.Outcome != resultv1alpha1.OutcomeSucceeded {
		t.Fatalf("unexpected result %#v", result)
	}
	if result.Envelope.Execution == nil ||
		result.Envelope.Execution.Model != "vendor/model@2026:beta" {
		t.Fatalf("model identifier changed: %#v", result.Envelope.Execution)
	}
	if got := resultv1alpha1.ProjectLegacyResult(result.Envelope.Output); got !=
		"Fake provider completed goal: explain the contract" {
		t.Fatalf("unexpected output %q", got)
	}
	if result.Envelope.Usage == nil || result.Envelope.Usage.TotalTokens == nil {
		t.Fatalf("expected normalized usage: %#v", result.Envelope.Usage)
	}
	if !emitter.has(events.TypeLifecycle) ||
		!emitter.has(events.TypeUsage) ||
		!emitter.has(events.TypeOutput) {
		t.Fatalf("missing execution events %#v", emitter.types)
	}
}

func TestRunnerNormalizesProviderFailures(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
		code     string
	}{
		{name: "failure", scenario: provider.FakeScenarioFailure, code: "fake_provider_failure"},
		{name: "malformed", scenario: provider.FakeScenarioMalformed, code: "invalid_provider_response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeConfig := baseConfig()
			runtimeConfig.FakeScenario = test.scenario
			result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
				context.Background(),
				runtimeConfig,
			)
			if result.ExitCode == 0 || result.Envelope.Outcome != resultv1alpha1.OutcomeFailed {
				t.Fatalf("expected failed execution, got %#v", result)
			}
			if result.Envelope.Error == nil || result.Envelope.Error.Code != test.code {
				t.Fatalf("unexpected error %#v", result.Envelope.Error)
			}
		})
	}
}

func TestRunnerRejectsUnsupportedProvider(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.Provider = "unknown"
	result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
		context.Background(),
		runtimeConfig,
	)
	if result.Envelope.Error == nil || result.Envelope.Error.Code != "unsupported_provider" {
		t.Fatalf("unexpected error %#v", result.Envelope.Error)
	}
}

func TestRunnerDistinguishesInvalidProviderConfiguration(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.FakeScenario = "unknown"
	result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
		context.Background(),
		runtimeConfig,
	)
	if result.Envelope.Error == nil ||
		result.Envelope.Error.Code != "invalid_provider_configuration" {
		t.Fatalf("unexpected error %#v", result.Envelope.Error)
	}
}

func TestRunnerHasNoImplicitWallclockDeadline(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.FakeScenario = provider.FakeScenarioDelay
	runtimeConfig.FakeDelay = 30 * time.Millisecond
	runtimeConfig.WallclockBudget = nil

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(ctx, runtimeConfig)
	if result.ExitCode != 0 {
		t.Fatalf("delay should succeed without configured wallclock: %#v", result.Envelope.Error)
	}
}

func TestRunnerLeavesWallclockAuthorityWithController(t *testing.T) {
	runtimeConfig := baseConfig()
	runtimeConfig.FakeScenario = provider.FakeScenarioDelay
	runtimeConfig.FakeDelay = 30 * time.Millisecond
	wallclock := 10 * time.Millisecond
	runtimeConfig.WallclockBudget = &wallclock

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(ctx, runtimeConfig)
	if result.ExitCode != 0 {
		t.Fatalf("runtime must not race controller wallclock enforcement: %#v", result.Envelope.Error)
	}
}

func TestRunnerHandlesParentDeadlineAndCancellation(t *testing.T) {
	t.Run("parent deadline", func(t *testing.T) {
		runtimeConfig := baseConfig()
		runtimeConfig.FakeScenario = provider.FakeScenarioDelay
		runtimeConfig.FakeDelay = time.Second
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(
			ctx,
			runtimeConfig,
		)
		if result.Envelope.Error == nil || result.Envelope.Error.Code != "deadline_exceeded" {
			t.Fatalf("unexpected deadline result %#v", result.Envelope.Error)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		runtimeConfig := baseConfig()
		runtimeConfig.FakeScenario = provider.FakeScenarioDelay
		runtimeConfig.FakeDelay = time.Second
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result := (engine.Runner{Emitter: &recordingEmitter{}}).Run(ctx, runtimeConfig)
		if result.Envelope.Error == nil || result.Envelope.Error.Code != "cancelled" {
			t.Fatalf("unexpected cancellation result %#v", result.Envelope.Error)
		}
	})
}

func baseConfig() config.Config {
	return config.Config{
		RunName:      "run-1",
		AgentName:    "agent-1",
		Goal:         "explain the contract",
		Provider:     "fake",
		Model:        "vendor/model@2026:beta",
		Tools:        []string{"unused-tool"},
		FakeScenario: provider.FakeScenarioSuccess,
	}
}

type recordingEmitter struct {
	types []events.Type
}

func (emitter *recordingEmitter) Emit(eventType events.Type, _ any) {
	emitter.types = append(emitter.types, eventType)
}

func (emitter *recordingEmitter) has(eventType events.Type) bool {
	for _, candidate := range emitter.types {
		if candidate == eventType {
			return true
		}
	}
	return false
}
