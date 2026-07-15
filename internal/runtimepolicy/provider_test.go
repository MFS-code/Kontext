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
		"anthropic": "ANTHROPIC_API_KEY",
		"openai":    "OPENAI_API_KEY",
		"google":    "GOOGLE_API_KEY",
		"gemini":    "GOOGLE_API_KEY",
		"mistral":   "MISTRAL_API_KEY",
		"groq":      "GROQ_API_KEY",
		"cohere":    "COHERE_API_KEY",
	}
	for provider, envName := range cases {
		creds := runtimepolicy.Credentials(provider)
		if len(creds) != 1 || creds[0].EnvVarName != envName {
			t.Fatalf("provider %s: expected env %s, got %#v", provider, envName, creds)
		}
	}
}

func TestCredentialsForAzureOpenAI(t *testing.T) {
	for _, provider := range []string{"azure", "azure-openai"} {
		creds := runtimepolicy.Credentials(provider)
		if len(creds) != 2 {
			t.Fatalf("provider %s: expected two credentials, got %#v", provider, creds)
		}
		if creds[0].EnvVarName != "AZURE_OPENAI_API_KEY" || creds[1].EnvVarName != "AZURE_OPENAI_ENDPOINT" {
			t.Fatalf("provider %s: unexpected credentials: %#v", provider, creds)
		}
	}
}

func TestCredentialsForBedrock(t *testing.T) {
	creds := runtimepolicy.Credentials("bedrock")
	if len(creds) != 2 {
		t.Fatalf("expected two Bedrock credentials, got %#v", creds)
	}
	if creds[0].EnvVarName != "AWS_ACCESS_KEY_ID" || creds[1].EnvVarName != "AWS_SECRET_ACCESS_KEY" {
		t.Fatalf("unexpected Bedrock credentials: %#v", creds)
	}
}

func TestCredentialsFallbackForUnknownProvider(t *testing.T) {
	creds := runtimepolicy.Credentials("custom-vendor")
	if len(creds) != 1 || creds[0].EnvVarName != "CUSTOM_VENDOR_API_KEY" {
		t.Fatalf("unexpected fallback credentials: %#v", creds)
	}
	if got := runtimepolicy.SecretName("custom-vendor", nil); got != "kontext-custom-vendor" {
		t.Fatalf("unexpected fallback secret: %s", got)
	}
}

func TestSecretNamePrefersRef(t *testing.T) {
	got := runtimepolicy.SecretName("openai", &kontextv1alpha1.SecretRef{Name: "my-secret"})
	if got != "my-secret" {
		t.Fatalf("expected my-secret, got %s", got)
	}
}
