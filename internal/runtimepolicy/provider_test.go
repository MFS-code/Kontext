package runtimepolicy_test

import (
	"testing"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/runtimepolicy"
)

func TestNormalizeProviderDefaults(t *testing.T) {
	if got := runtimepolicy.NormalizeProvider(""); got != "anthropic" {
		t.Fatalf("expected anthropic default, got %q", got)
	}
	if got := runtimepolicy.NormalizeProvider("OpenAI"); got != "openai" {
		t.Fatalf("expected openai, got %q", got)
	}
}

func TestNeedsAPIKey(t *testing.T) {
	for _, provider := range []string{"echo", "fake", "deterministic", "replay"} {
		if runtimepolicy.NeedsAPIKey(provider) {
			t.Fatalf("expected %s to be keyless", provider)
		}
	}
	for _, provider := range []string{"anthropic", "openai", "google", "gemini", "azure-openai", "mistral", "groq", "cohere", "bedrock"} {
		if !runtimepolicy.NeedsAPIKey(provider) {
			t.Fatalf("expected %s to require a key", provider)
		}
	}
}

func TestCredentialsForKnownProviders(t *testing.T) {
	cases := map[string]string{
		"anthropic":    "ANTHROPIC_API_KEY",
		"openai":       "OPENAI_API_KEY",
		"google":       "GOOGLE_API_KEY",
		"gemini":       "GOOGLE_API_KEY",
		"azure-openai": "AZURE_OPENAI_API_KEY",
		"mistral":      "MISTRAL_API_KEY",
		"groq":         "GROQ_API_KEY",
		"cohere":       "COHERE_API_KEY",
		"bedrock":      "AWS_ACCESS_KEY_ID",
	}
	for provider, envName := range cases {
		creds := runtimepolicy.Credentials(provider)
		if creds.EnvVarName != envName {
			t.Fatalf("provider %s: expected env %s, got %s", provider, envName, creds.EnvVarName)
		}
	}
}

func TestCredentialsFallbackForUnknownProvider(t *testing.T) {
	creds := runtimepolicy.Credentials("custom-vendor")
	if creds.EnvVarName != "CUSTOM_VENDOR_API_KEY" {
		t.Fatalf("unexpected fallback env: %s", creds.EnvVarName)
	}
	if creds.DefaultSecretName != "kontext-custom-vendor" {
		t.Fatalf("unexpected fallback secret: %s", creds.DefaultSecretName)
	}
}

func TestSecretNamePrefersRef(t *testing.T) {
	got := runtimepolicy.SecretName("openai", &kontextv1alpha1.SecretRef{Name: "my-secret"})
	if got != "my-secret" {
		t.Fatalf("expected my-secret, got %s", got)
	}
}
