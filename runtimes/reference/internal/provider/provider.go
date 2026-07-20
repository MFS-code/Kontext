package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

type Provider interface {
	Name() string
	Complete(context.Context, runtimeapi.CompletionRequest) (runtimeapi.CompletionResponse, error)
}

type UnsupportedError struct {
	Name string
}

func (err *UnsupportedError) Error() string {
	return fmt.Sprintf("unsupported provider %q", err.Name)
}

type ConfigurationError struct {
	Provider string
	Code     string
	Message  string
}

func (err *ConfigurationError) Error() string {
	if err.Provider == "" {
		return fmt.Sprintf("provider configuration is invalid: %s", err.Message)
	}
	return fmt.Sprintf("%s provider configuration is invalid: %s", err.Provider, err.Message)
}

func Resolve(runtimeConfig config.Config) (Provider, error) {
	switch runtimeConfig.Provider {
	case "fake":
		return resolveFake(runtimeConfig)
	case "anthropic":
		return NewAnthropic(AnthropicConfig{
			APIKey:   runtimeConfig.AnthropicAPIKey,
			Endpoint: runtimeConfig.ProviderEndpoint,
			BaseURL:  runtimeConfig.ProviderBaseURL,
			Client:   http.DefaultClient,
		})
	case "openai", "openai-compatible":
		return NewOpenAI(OpenAIConfig{
			Name:     runtimeConfig.Provider,
			APIKey:   runtimeConfig.OpenAIAPIKey,
			Endpoint: runtimeConfig.ProviderEndpoint,
			BaseURL:  runtimeConfig.ProviderBaseURL,
			Client:   http.DefaultClient,
		})
	default:
		return nil, &UnsupportedError{Name: runtimeConfig.Provider}
	}
}

func providerToolResultContent(result *runtimeapi.ToolResult) string {
	payload := struct {
		Content   string `json:"content"`
		IsError   bool   `json:"isError"`
		ErrorCode string `json:"errorCode,omitempty"`
		Truncated bool   `json:"truncated,omitempty"`
	}{
		Content:   result.Content,
		IsError:   result.IsError,
		ErrorCode: result.ErrorCode,
		Truncated: result.Truncated,
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}
