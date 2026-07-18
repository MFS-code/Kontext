package runtimepolicy

import (
	"strings"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
)

const DefaultProvider = "anthropic"

// CredentialSpec describes how a provider receives API keys in the runtime Pod.
type CredentialSpec struct {
	EnvVarName string
	SecretKey  string
}

type providerDefinition struct {
	keyless           bool
	defaultSecretName string
	credentials       []CredentialSpec
}

var providerDefinitions = map[string]providerDefinition{
	"anthropic": {
		defaultSecretName: "kontext-anthropic",
		credentials: []CredentialSpec{
			{EnvVarName: "ANTHROPIC_API_KEY", SecretKey: "ANTHROPIC_API_KEY"},
		},
	},
	"openai": {
		defaultSecretName: "kontext-openai",
		credentials: []CredentialSpec{
			{EnvVarName: "OPENAI_API_KEY", SecretKey: "OPENAI_API_KEY"},
		},
	},
	"openai-compatible": {
		defaultSecretName: "kontext-openai",
		credentials: []CredentialSpec{
			{EnvVarName: "OPENAI_API_KEY", SecretKey: "OPENAI_API_KEY"},
		},
	},
	"google": {
		defaultSecretName: "kontext-google",
		credentials: []CredentialSpec{
			{EnvVarName: "GOOGLE_API_KEY", SecretKey: "GOOGLE_API_KEY"},
		},
	},
	"gemini": {
		defaultSecretName: "kontext-google",
		credentials: []CredentialSpec{
			{EnvVarName: "GOOGLE_API_KEY", SecretKey: "GOOGLE_API_KEY"},
		},
	},
	"azure": {
		defaultSecretName: "kontext-azure-openai",
		credentials: []CredentialSpec{
			{EnvVarName: "AZURE_OPENAI_API_KEY", SecretKey: "AZURE_OPENAI_API_KEY"},
			{EnvVarName: "AZURE_OPENAI_ENDPOINT", SecretKey: "AZURE_OPENAI_ENDPOINT"},
		},
	},
	"azure-openai": {
		defaultSecretName: "kontext-azure-openai",
		credentials: []CredentialSpec{
			{EnvVarName: "AZURE_OPENAI_API_KEY", SecretKey: "AZURE_OPENAI_API_KEY"},
			{EnvVarName: "AZURE_OPENAI_ENDPOINT", SecretKey: "AZURE_OPENAI_ENDPOINT"},
		},
	},
	"mistral": {
		defaultSecretName: "kontext-mistral",
		credentials: []CredentialSpec{
			{EnvVarName: "MISTRAL_API_KEY", SecretKey: "MISTRAL_API_KEY"},
		},
	},
	"groq": {
		defaultSecretName: "kontext-groq",
		credentials: []CredentialSpec{
			{EnvVarName: "GROQ_API_KEY", SecretKey: "GROQ_API_KEY"},
		},
	},
	"cohere": {
		defaultSecretName: "kontext-cohere",
		credentials: []CredentialSpec{
			{EnvVarName: "COHERE_API_KEY", SecretKey: "COHERE_API_KEY"},
		},
	},
	"bedrock": {
		defaultSecretName: "kontext-bedrock",
		credentials: []CredentialSpec{
			{EnvVarName: "AWS_ACCESS_KEY_ID", SecretKey: "AWS_ACCESS_KEY_ID"},
			{EnvVarName: "AWS_SECRET_ACCESS_KEY", SecretKey: "AWS_SECRET_ACCESS_KEY"},
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
func Credentials(provider string) []CredentialSpec {
	normalized := NormalizeProvider(provider)
	def, ok := providerDefinitions[normalized]
	if !ok {
		envName := strings.ToUpper(strings.ReplaceAll(normalized, "-", "_")) + "_API_KEY"
		return []CredentialSpec{
			{EnvVarName: envName, SecretKey: envName},
		}
	}
	if def.keyless {
		return nil
	}
	return append([]CredentialSpec(nil), def.credentials...)
}

// SecretName returns the Kubernetes Secret name for provider credentials.
func SecretName(provider string, secretRef *kontextv1alpha1.SecretRef) string {
	if secretRef != nil && secretRef.Name != "" {
		return secretRef.Name
	}
	normalized := NormalizeProvider(provider)
	if def, ok := providerDefinitions[normalized]; ok {
		return def.defaultSecretName
	}
	return "kontext-" + normalized
}
