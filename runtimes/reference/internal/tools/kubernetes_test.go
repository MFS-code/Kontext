package tools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/tools"
)

func TestKubernetesReadUsesCurrentNamespaceAndServiceAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet ||
			request.URL.Path != "/api/v1/namespaces/agent-space/pods/pod-1" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer service-account-token" {
			t.Errorf("unexpected authorization header")
		}
		_, _ = writer.Write([]byte(`{"kind":"Pod","metadata":{"name":"pod-1"}}`))
	}))
	defer server.Close()
	registry, err := tools.New(tools.Config{
		Allowed: []string{tools.NameKubernetesRead},
		Kubernetes: tools.KubernetesConfig{
			BaseURL:   server.URL,
			Namespace: "agent-space",
			Token:     "service-account-token",
			Client:    server.Client(),
		},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "kube-1",
		Name:      tools.NameKubernetesRead,
		Arguments: json.RawMessage(`{"operation":"get","resource":"pods","name":"pod-1"}`),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError || !json.Valid([]byte(result.Content)) {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestKubernetesReadBoundsListsAndNeverAllowsSecrets(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/apis/apps/v1/namespaces/current/deployments" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		if request.URL.Query().Get("limit") != "50" {
			t.Errorf("expected bounded list limit")
		}
		_, _ = writer.Write([]byte(`{"kind":"DeploymentList","items":[]}`))
	}))
	defer server.Close()
	registry, err := tools.New(tools.Config{
		Allowed: []string{tools.NameKubernetesRead},
		Kubernetes: tools.KubernetesConfig{
			BaseURL:   server.URL,
			Namespace: "current",
			Token:     "token",
			Client:    server.Client(),
		},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	listResult, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "kube-list",
		Name:      tools.NameKubernetesRead,
		Arguments: json.RawMessage(`{"operation":"list","resource":"deployments"}`),
	})
	if err != nil || listResult.IsError {
		t.Fatalf("list failed: result=%#v err=%v", listResult, err)
	}
	secretResult, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "kube-secret",
		Name:      tools.NameKubernetesRead,
		Arguments: json.RawMessage(`{"operation":"list","resource":"secrets"}`),
	})
	if err != nil {
		t.Fatalf("secret request: %v", err)
	}
	if !secretResult.IsError ||
		secretResult.ErrorCode != "kubernetes_resource_denied" {
		t.Fatalf("unexpected secret result %#v", secretResult)
	}
	pathResult, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "kube-path",
		Name:      tools.NameKubernetesRead,
		Arguments: json.RawMessage(`{"operation":"get","resource":"pods","name":".."}`),
	})
	if err != nil {
		t.Fatalf("path request: %v", err)
	}
	if !pathResult.IsError || pathResult.ErrorCode != "invalid_tool_arguments" {
		t.Fatalf("unexpected path result %#v", pathResult)
	}
	if requests != 1 {
		t.Fatalf("secret request reached API server; requests=%d", requests)
	}
}

func TestKubernetesReadReturnsRBACDenialToModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"message":"forbidden"}`))
	}))
	defer server.Close()
	registry, err := tools.New(tools.Config{
		Allowed:          []string{tools.NameKubernetesRead},
		MaxCapturedBytes: 8,
		Kubernetes: tools.KubernetesConfig{
			BaseURL:   server.URL,
			Namespace: "current",
			Token:     "token",
			Client:    server.Client(),
		},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "kube-denied",
		Name:      tools.NameKubernetesRead,
		Arguments: json.RawMessage(`{"operation":"get","resource":"pods","name":"pod-1"}`),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError ||
		result.ErrorCode != "kubernetes_rbac_denied" ||
		!result.Truncated ||
		len(result.Content) != 8 {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestKubernetesReadBuildsIPv6ServiceURL(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "fd00::1")
	t.Setenv("KUBERNETES_SERVICE_PORT_HTTPS", "443")
	var receivedURL string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		receivedURL = request.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"kind":"PodList","items":[]}`)),
		}, nil
	})}
	registry, err := tools.New(tools.Config{
		Allowed: []string{tools.NameKubernetesRead},
		Kubernetes: tools.KubernetesConfig{
			Namespace: "current",
			Token:     "token",
			Client:    client,
		},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), runtimeapi.ToolCall{
		ID:        "kube-ipv6",
		Name:      tools.NameKubernetesRead,
		Arguments: json.RawMessage(`{"operation":"list","resource":"pods"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("execute: result=%#v err=%v", result, err)
	}
	if !strings.HasPrefix(receivedURL, "https://[fd00::1]:443/") {
		t.Fatalf("IPv6 host was not bracketed: %q", receivedURL)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
