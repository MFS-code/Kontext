package runtimepolicy

import (
	"strings"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
)

const DefaultProvider = "anthropic"

// CredentialSpec describes how a provider receives API keys in the runtime Pod.
type CredentialSpec struct {
	EnvVarName        string
	SecretKey         string
	DefaultSecretName string
}

type providerDefinition struct {
	keyless     bool
	credentials CredentialSpec
}

var providerDefinitions = map[string]providerDefinition{
	"anthropic": {
		credentials: CredentialSpec{
			EnvVarName:        "ANTHROPIC_API_KEY",
			SecretKey:         "ANTHROPIC_API_KEY",
			DefaultSecretName: "kontext-anthropic",
		},
	},
	"openai": {
		credentials: CredentialSpec{
			EnvVarName:        "OPENAI_API_KEY",
			SecretKey:         "OPENAI_API_KEY",
			DefaultSecretName: "kontext-openai",
		},
	},
	"google": {
		credentials: CredentialSpec{
			EnvVarName:        "GOOGLE_API_KEY",
			SecretKey:         "GOOGLE_API_KEY",
			DefaultSecretName: "kontext-google",
		},
	},
	"gemini": {
		credentials: CredentialSpec{
			EnvVarName:        "GOOGLE_API_KEY",
			SecretKey:         "GOOGLE_API_KEY",
			DefaultSecretName: "kontext-google",
		},
	},
	"azure": {
		credentials: CredentialSpec{
			EnvVarName:        "AZURE_OPENAI_API_KEY",
			SecretKey:         "AZURE_OPENAI_API_KEY",
			DefaultSecretName: "kontext-azure-openai",
		},
	},
	"azure-openai": {
		credentials: CredentialSpec{
			EnvVarName:        "AZURE_OPENAI_API_KEY",
			SecretKey:         "AZURE_OPENAI_API_KEY",
			DefaultSecretName: "kontext-azure-openai",
		},
	},
	"mistral": {
		credentials: CredentialSpec{
			EnvVarName:        "MISTRAL_API_KEY",
			SecretKey:         "MISTRAL_API_KEY",
			DefaultSecretName: "kontext-mistral",
		},
	},
	"groq": {
		credentials: CredentialSpec{
			EnvVarName:        "GROQ_API_KEY",
			SecretKey:         "GROQ_API_KEY",
			DefaultSecretName: "kontext-groq",
		},
	},
	"cohere": {
		credentials: CredentialSpec{
			EnvVarName:        "COHERE_API_KEY",
			SecretKey:         "COHERE_API_KEY",
			DefaultSecretName: "kontext-cohere",
		},
	},
	"bedrock": {
		credentials: CredentialSpec{
			EnvVarName:        "AWS_ACCESS_KEY_ID",
			SecretKey:         "AWS_ACCESS_KEY_ID",
			DefaultSecretName: "kontext-bedrock",
		},
	},
	"fake":          {keyless: true},
	"replay":        {keyless: true},
	"echo":          {keyless: true},
	"deterministic": {keyless: true},
}

// NormalizeProvider returns the canonical provider name.
func NormalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return DefaultProvider
	}
	return provider
}

// NeedsAPIKey reports whether the provider requires a credentials secret.
func NeedsAPIKey(provider string) bool {
	def, ok := providerDefinitions[NormalizeProvider(provider)]
	if !ok {
		return true
	}
	return !def.keyless
}

// Credentials returns the credential wiring for a provider.
func Credentials(provider string) CredentialSpec {
	normalized := NormalizeProvider(provider)
	def, ok := providerDefinitions[normalized]
	if !ok {
		envName := strings.ToUpper(strings.ReplaceAll(normalized, "-", "_")) + "_API_KEY"
		return CredentialSpec{
			EnvVarName:        envName,
			SecretKey:         envName,
			DefaultSecretName: "kontext-" + normalized,
		}
	}
	if def.keyless {
		return CredentialSpec{}
	}
	return def.credentials
}

// SecretName returns the Kubernetes Secret name for provider credentials.
func SecretName(provider string, secretRef *kontextv1alpha1.SecretRef) string {
	if secretRef != nil && secretRef.Name != "" {
		return secretRef.Name
	}
	return Credentials(provider).DefaultSecretName
}
