package tools

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

const (
	serviceAccountRoot      = "/var/run/secrets/kubernetes.io/serviceaccount"
	serviceAccountTokenPath = serviceAccountRoot + "/token"
	serviceAccountCAPath    = serviceAccountRoot + "/ca.crt"
	serviceAccountNSPath    = serviceAccountRoot + "/namespace"
)

type KubernetesConfig struct {
	BaseURL   string
	Namespace string
	Token     string
	Client    *http.Client
}

type kubernetesTool struct {
	config   KubernetesConfig
	maxBytes int64

	clientOnce sync.Once
	client     *http.Client
	clientErr  error
}

type kubernetesReadArguments struct {
	Operation string `json:"operation"`
	Resource  string `json:"resource"`
	Name      string `json:"name,omitempty"`
}

type kubernetesResourceDefinition struct {
	name      string
	apiPrefix string
}

var kubernetesResourceRegistry = []kubernetesResourceDefinition{
	{name: "pods", apiPrefix: "/api/v1"},
	{name: "configmaps", apiPrefix: "/api/v1"},
	{name: "services", apiPrefix: "/api/v1"},
	{name: "events", apiPrefix: "/api/v1"},
	{name: "deployments", apiPrefix: "/apis/apps/v1"},
	{name: "statefulsets", apiPrefix: "/apis/apps/v1"},
	{name: "daemonsets", apiPrefix: "/apis/apps/v1"},
	{name: "replicasets", apiPrefix: "/apis/apps/v1"},
	{name: "jobs", apiPrefix: "/apis/batch/v1"},
	{name: "cronjobs", apiPrefix: "/apis/batch/v1"},
	{name: "agents", apiPrefix: "/apis/kontext.dev/v1alpha1"},
	{name: "agentruns", apiPrefix: "/apis/kontext.dev/v1alpha1"},
}

var kubernetesResources = indexKubernetesResources(kubernetesResourceRegistry)

var kubernetesNamePattern = regexp.MustCompile(
	`^[a-z0-9]([-.a-z0-9]*[a-z0-9])?$`,
)

func newKubernetesTool(config KubernetesConfig, maxBytes int64) *kubernetesTool {
	return &kubernetesTool{config: config, maxBytes: maxBytes}
}

func (tool *kubernetesTool) Definition() runtimeapi.ToolDefinition {
	return runtimeapi.ToolDefinition{
		Name:        NameKubernetesRead,
		Description: "Get or list an allowlisted Kubernetes resource in the current namespace. Secrets are never available.",
		InputSchema: kubernetesReadInputSchema(),
	}
}

func indexKubernetesResources(
	definitions []kubernetesResourceDefinition,
) map[string]kubernetesResourceDefinition {
	resources := make(map[string]kubernetesResourceDefinition, len(definitions))
	for _, definition := range definitions {
		if definition.name == "" || definition.apiPrefix == "" {
			panic("Kubernetes resource definitions require a name and API prefix")
		}
		if definition.name == "secrets" {
			panic("Kubernetes Secrets must never be exposed")
		}
		if _, duplicate := resources[definition.name]; duplicate {
			panic(fmt.Sprintf("duplicate Kubernetes resource %q", definition.name))
		}
		resources[definition.name] = definition
	}
	return resources
}

func kubernetesReadInputSchema() json.RawMessage {
	resourceNames := make([]string, 0, len(kubernetesResourceRegistry))
	for _, resource := range kubernetesResourceRegistry {
		resourceNames = append(resourceNames, resource.name)
	}
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type": "string",
				"enum": []string{"get", "list"},
			},
			"resource": map[string]any{
				"type": "string",
				"enum": resourceNames,
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Required for get; omit for list",
			},
		},
		"required":             []string{"operation", "resource"},
		"additionalProperties": false,
	})
	if err != nil {
		panic(fmt.Sprintf("encode kubernetes_read schema: %v", err))
	}
	return schema
}

func (tool *kubernetesTool) Execute(
	ctx context.Context,
	rawArguments []byte,
) (outcome, error) {
	var arguments kubernetesReadArguments
	if err := decodeArguments(rawArguments, &arguments); err != nil {
		return outcome{}, err
	}
	resourceName := strings.ToLower(strings.TrimSpace(arguments.Resource))
	resource, allowed := kubernetesResources[resourceName]
	if !allowed {
		return outcome{}, &Error{
			Code:    "kubernetes_resource_denied",
			Message: fmt.Sprintf("Kubernetes resource %q is not allowlisted", arguments.Resource),
		}
	}
	operation := strings.ToLower(strings.TrimSpace(arguments.Operation))
	switch operation {
	case "get":
		name := strings.TrimSpace(arguments.Name)
		if name == "" {
			return outcome{}, &Error{
				Code:    "invalid_tool_arguments",
				Message: "name is required for Kubernetes get",
			}
		}
		if len(name) > 253 || !kubernetesNamePattern.MatchString(name) {
			return outcome{}, &Error{
				Code:    "invalid_tool_arguments",
				Message: "name must be a valid Kubernetes DNS subdomain",
			}
		}
		arguments.Name = name
	case "list":
		if strings.TrimSpace(arguments.Name) != "" {
			return outcome{}, &Error{
				Code:    "invalid_tool_arguments",
				Message: "name must be omitted for Kubernetes list",
			}
		}
	default:
		return outcome{}, &Error{
			Code:    "kubernetes_operation_denied",
			Message: "operation must be get or list",
		}
	}

	config, err := tool.resolveConfig()
	if err != nil {
		return outcome{}, err
	}
	requestPath := path.Join(
		resource.apiPrefix,
		"namespaces",
		url.PathEscape(config.Namespace),
		resourceName,
	)
	if operation == "get" {
		requestPath = path.Join(requestPath, url.PathEscape(arguments.Name))
	}
	endpoint := strings.TrimRight(config.BaseURL, "/") + requestPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return outcome{}, &Error{
			Code:    "kubernetes_request_failed",
			Message: fmt.Sprintf("create Kubernetes request: %v", err),
		}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+config.Token)
	if operation == "list" {
		query := request.URL.Query()
		query.Set("limit", "50")
		request.URL.RawQuery = query.Encode()
	}

	response, err := config.Client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return outcome{}, ctx.Err()
		}
		return outcome{}, &Error{
			Code:    "kubernetes_request_failed",
			Message: fmt.Sprintf("Kubernetes API request failed: %v", err),
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, tool.maxBytes+1))
	if err != nil {
		return outcome{}, &Error{
			Code:    "kubernetes_response_failed",
			Message: fmt.Sprintf("read Kubernetes API response: %v", err),
		}
	}
	truncated := int64(len(body)) > tool.maxBytes
	if truncated {
		body = body[:tool.maxBytes]
		body, err = json.Marshal(struct {
			Partial string `json:"partial"`
		}{Partial: string(body)})
		if err != nil {
			return outcome{}, &Error{
				Code:    "kubernetes_response_failed",
				Message: fmt.Sprintf("encode truncated Kubernetes response: %v", err),
			}
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		code := "kubernetes_request_rejected"
		switch response.StatusCode {
		case http.StatusUnauthorized:
			code = "kubernetes_authentication_failed"
		case http.StatusForbidden:
			code = "kubernetes_rbac_denied"
		case http.StatusNotFound:
			code = "kubernetes_not_found"
		}
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = fmt.Sprintf("Kubernetes API returned HTTP %d", response.StatusCode)
		}
		return outcome{
			Content:   message,
			IsError:   true,
			ErrorCode: code,
			Truncated: truncated,
		}, nil
	}
	if !truncated && !json.Valid(body) {
		return outcome{}, &Error{
			Code:    "kubernetes_invalid_response",
			Message: "Kubernetes API returned invalid JSON",
		}
	}
	return outcome{
		Content:   string(body),
		Truncated: truncated,
	}, nil
}

func (tool *kubernetesTool) resolveConfig() (KubernetesConfig, error) {
	config := tool.config
	if config.Client == nil {
		tool.clientOnce.Do(func() {
			tool.client, tool.clientErr = newKubernetesClient()
		})
		if tool.clientErr != nil {
			return KubernetesConfig{}, tool.clientErr
		}
		config.Client = tool.client
	}
	return resolveKubernetesConfig(config)
}

func resolveKubernetesConfig(config KubernetesConfig) (KubernetesConfig, error) {
	if config.Namespace == "" {
		namespace, err := os.ReadFile(serviceAccountNSPath)
		if err != nil {
			return KubernetesConfig{}, &Error{
				Code:    "kubernetes_unavailable",
				Message: fmt.Sprintf("read current namespace: %v", err),
			}
		}
		config.Namespace = strings.TrimSpace(string(namespace))
	}
	if config.Token == "" {
		token, err := os.ReadFile(serviceAccountTokenPath)
		if err != nil {
			return KubernetesConfig{}, &Error{
				Code:    "kubernetes_unavailable",
				Message: fmt.Sprintf("read service-account token: %v", err),
			}
		}
		config.Token = strings.TrimSpace(string(token))
	}
	if config.BaseURL == "" {
		host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
		port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"))
		if port == "" {
			port = strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT"))
		}
		if host == "" || port == "" {
			return KubernetesConfig{}, &Error{
				Code:    "kubernetes_unavailable",
				Message: "Kubernetes service host and port are unavailable",
			}
		}
		config.BaseURL = "https://" + net.JoinHostPort(host, port)
	}
	if config.Client == nil {
		var err error
		config.Client, err = newKubernetesClient()
		if err != nil {
			return KubernetesConfig{}, err
		}
	}
	if config.Namespace == "" || config.Token == "" {
		return KubernetesConfig{}, &Error{
			Code:    "kubernetes_unavailable",
			Message: "Kubernetes namespace and service-account token are required",
		}
	}
	return config, nil
}

func newKubernetesClient() (*http.Client, error) {
	certificate, err := os.ReadFile(serviceAccountCAPath)
	if err != nil {
		return nil, &Error{
			Code:    "kubernetes_unavailable",
			Message: fmt.Sprintf("read Kubernetes CA certificate: %v", err),
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return nil, &Error{
			Code:    "kubernetes_unavailable",
			Message: "Kubernetes CA certificate is invalid",
		}
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}}, nil
}
