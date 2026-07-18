package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

const maxProviderResponseBytes = 4 << 20

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type httpResult struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func sendJSON(
	ctx context.Context,
	client HTTPClient,
	endpoint string,
	headers http.Header,
	payload any,
) (httpResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return httpResult{}, fmt.Errorf("encode provider request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return httpResult{}, &ConfigurationError{
			Code:    "invalid_provider_endpoint",
			Message: fmt.Sprintf("cannot create request for configured endpoint: %v", err),
		}
	}
	request.Header.Set("Content-Type", "application/json")
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}

	response, err := client.Do(request)
	if err != nil {
		return httpResult{}, normalizeRequestError(ctx, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
	if err != nil {
		retryable := true
		return httpResult{}, &runtimeapi.ProviderError{
			Code:      "provider_response_read_failed",
			Message:   fmt.Sprintf("could not read provider response: %v", err),
			Retryable: &retryable,
			RequestID: requestID(response.Header),
		}
	}
	if len(responseBody) > maxProviderResponseBytes {
		retryable := false
		status := response.StatusCode
		return httpResult{}, &runtimeapi.ProviderError{
			Code:       "invalid_provider_response",
			Message:    fmt.Sprintf("provider response exceeded %d bytes", maxProviderResponseBytes),
			Retryable:  &retryable,
			HTTPStatus: &status,
			RequestID:  requestID(response.Header),
		}
	}
	return httpResult{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       responseBody,
	}, nil
}

func normalizeRequestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &networkError) && networkError.Timeout()) {
		retryable := true
		return &runtimeapi.ProviderError{
			Code:      "provider_timeout",
			Message:   "provider request timed out",
			Retryable: &retryable,
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	retryable := true
	return &runtimeapi.ProviderError{
		Code:      "provider_network_error",
		Message:   fmt.Sprintf("provider endpoint request failed: %v", err),
		Retryable: &retryable,
	}
}

func providerHTTPError(
	providerName string,
	status int,
	message string,
	requestIDValue string,
) error {
	code := "provider_endpoint_error"
	retryable := false
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		code = "authentication_failed"
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		code = "provider_timeout"
		retryable = true
	case status == http.StatusTooManyRequests:
		code = "rate_limited"
		retryable = true
	case status >= 500:
		code = "provider_endpoint_error"
		retryable = true
	case status >= 400 && status < 500:
		code = "provider_request_rejected"
	}
	if strings.TrimSpace(message) == "" {
		message = fmt.Sprintf("%s endpoint returned HTTP %d", providerName, status)
	}
	return &runtimeapi.ProviderError{
		Code:       code,
		Message:    message,
		Retryable:  &retryable,
		HTTPStatus: &status,
		RequestID:  requestIDValue,
	}
}

func invalidResponse(
	message string,
	status int,
	requestIDValue string,
) error {
	retryable := false
	return &runtimeapi.ProviderError{
		Code:       "invalid_provider_response",
		Message:    message,
		Retryable:  &retryable,
		HTTPStatus: &status,
		RequestID:  requestIDValue,
	}
}

func validateAPIKey(providerName string, apiKey string) error {
	if apiKey == "" {
		return &ConfigurationError{
			Provider: providerName,
			Code:     "missing_provider_credentials",
			Message:  fmt.Sprintf("%s_API_KEY is required", strings.ToUpper(providerName)),
		}
	}
	if strings.TrimSpace(apiKey) != apiKey ||
		strings.IndexFunc(apiKey, unicode.IsControl) >= 0 {
		return &ConfigurationError{
			Provider: providerName,
			Code:     "invalid_provider_credentials",
			Message:  fmt.Sprintf("%s_API_KEY must not contain surrounding whitespace or control characters", strings.ToUpper(providerName)),
		}
	}
	return nil
}

func providerEndpoint(
	providerName string,
	explicitEndpoint string,
	baseURL string,
	defaultEndpoint string,
	path string,
) (string, error) {
	if explicitEndpoint != "" && baseURL != "" {
		return "", &ConfigurationError{
			Provider: providerName,
			Code:     "invalid_provider_endpoint",
			Message:  "provider endpoint and base URL are mutually exclusive",
		}
	}
	if explicitEndpoint != "" {
		if err := validateHTTPURL(explicitEndpoint); err != nil {
			return "", &ConfigurationError{
				Provider: providerName,
				Code:     "invalid_provider_endpoint",
				Message:  fmt.Sprintf("invalid provider endpoint: %v", err),
			}
		}
		return explicitEndpoint, nil
	}
	if baseURL == "" {
		return defaultEndpoint, nil
	}
	if err := validateHTTPURL(baseURL); err != nil {
		return "", &ConfigurationError{
			Provider: providerName,
			Code:     "invalid_provider_endpoint",
			Message:  fmt.Sprintf("invalid provider base URL: %v", err),
		}
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", &ConfigurationError{
			Provider: providerName,
			Code:     "invalid_provider_endpoint",
			Message:  fmt.Sprintf("invalid provider base URL: %v", err),
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return parsed.String(), nil
}

func validateHTTPURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil {
		return errors.New("must not contain embedded credentials")
	}
	return nil
}

func requestID(header http.Header, names ...string) string {
	tried := make(map[string]struct{}, len(names))
	for _, name := range names {
		canonicalName := http.CanonicalHeaderKey(name)
		if _, found := tried[canonicalName]; found {
			continue
		}
		tried[canonicalName] = struct{}{}
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	for _, name := range []string{"request-id", "x-request-id"} {
		if _, found := tried[http.CanonicalHeaderKey(name)]; found {
			continue
		}
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
