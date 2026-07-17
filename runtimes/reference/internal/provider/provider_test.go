package provider_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/provider"
	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

func TestResolveSelectsFakeProvider(t *testing.T) {
	selected, err := provider.Resolve(config.Config{Provider: "fake"})
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if selected.Name() != "fake" {
		t.Fatalf("unexpected provider %q", selected.Name())
	}
	if _, err := provider.Resolve(config.Config{Provider: "unknown"}); err == nil {
		t.Fatalf("expected unsupported provider error")
	}
}

func TestResolveValidatesFakeScenarios(t *testing.T) {
	if _, err := provider.Resolve(config.Config{
		Provider:     "fake",
		FakeScenario: "other",
	}); err == nil {
		t.Fatalf("expected unsupported scenario error")
	}
	if _, err := provider.Resolve(config.Config{
		Provider:     "fake",
		FakeScenario: provider.FakeScenarioDelay,
	}); err == nil {
		t.Fatalf("expected missing delay error")
	}
}

func TestFakeProviderScenarios(t *testing.T) {
	request := completionRequest("vendor/model@2026:beta")

	t.Run("success", func(t *testing.T) {
		fake := &provider.Fake{Scenario: provider.FakeScenarioSuccess}
		response, err := fake.Complete(context.Background(), request)
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if err := runtimeapi.ValidateResponse(response); err != nil {
			t.Fatalf("validate response: %v", err)
		}
		if got := runtimeapi.MessageText(response.Message); got != "Fake provider completed goal: test goal" {
			t.Fatalf("unexpected output %q", got)
		}
		if response.Usage.InputTokens == nil || *response.Usage.InputTokens != 2 {
			t.Fatalf("unexpected input usage %#v", response.Usage)
		}
		if response.RequestID == "" {
			t.Fatalf("expected deterministic request id")
		}
	})

	t.Run("failure", func(t *testing.T) {
		fake := &provider.Fake{Scenario: provider.FakeScenarioFailure}
		_, err := fake.Complete(context.Background(), request)
		var providerError *runtimeapi.ProviderError
		if !errors.As(err, &providerError) || providerError.Code != "fake_provider_failure" {
			t.Fatalf("unexpected error %v", err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		fake := &provider.Fake{Scenario: provider.FakeScenarioMalformed}
		response, err := fake.Complete(context.Background(), request)
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if err := runtimeapi.ValidateResponse(response); err == nil {
			t.Fatalf("expected malformed response")
		}
	})

	t.Run("delay cancellation", func(t *testing.T) {
		fake := &provider.Fake{
			Scenario: provider.FakeScenarioDelay,
			Delay:    time.Second,
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		started := time.Now()
		_, err := fake.Complete(ctx, request)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
		if time.Since(started) > 100*time.Millisecond {
			t.Fatalf("fake provider did not cancel promptly")
		}
	})
}

func completionRequest(model string) runtimeapi.CompletionRequest {
	return runtimeapi.CompletionRequest{
		Model: model,
		Messages: []runtimeapi.Message{
			{
				Role: runtimeapi.RoleUser,
				Content: []runtimeapi.ContentBlock{
					{Type: runtimeapi.ContentTypeText, Text: "test goal"},
				},
			},
		},
	}
}
