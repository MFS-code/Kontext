package webhooktls

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestEnvtestRealTLSAndNarrowAdmissionBypass(t *testing.T) {
	testEnvironment := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			LocalServingHost:             "127.0.0.1",
			LocalServingHostExternalName: "localhost",
		},
	}
	config, err := testEnvironment.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := testEnvironment.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add Kubernetes scheme: %v", err)
	}
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Kontext scheme: %v", err)
	}
	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("create envtest client: %v", err)
	}
	ctx := context.Background()
	if err := k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultNamespace},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create webhook namespace: %v", err)
	}

	now := time.Now
	options := DefaultOptions()
	options.ServiceName = "localhost"
	options.Clock = now
	store := &Store{}
	lifecycle := NewLifecycle(k8sClient, store, options)
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("bootstrap self-managed TLS: %v", err)
	}

	server := webhook.NewServer(webhook.Options{
		Host: testEnvironment.WebhookInstallOptions.LocalServingHost,
		Port: testEnvironment.WebhookInstallOptions.LocalServingPort,
		TLSOpts: []func(*tls.Config){
			TLSOption(store),
		},
	})
	server.Register(DefaultWebhookPath, Handler())
	serverContext, cancelServer := context.WithCancel(ctx)
	t.Cleanup(cancelServer)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Start(serverContext)
	}()
	waitForWebhookServer(t, server)

	var registration admissionv1.MutatingWebhookConfiguration
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get registration: %v", err)
	}
	url := fmt.Sprintf(
		"https://localhost:%d%s",
		testEnvironment.WebhookInstallOptions.LocalServingPort,
		DefaultWebhookPath,
	)
	registration.Webhooks[0].ClientConfig.Service = nil
	registration.Webhooks[0].ClientConfig.URL = &url
	if err := k8sClient.Update(ctx, &registration); err != nil {
		t.Fatalf("point envtest registration at TLS server: %v", err)
	}

	complete := testAgentRun("complete-bypass", completeSpec())
	if err := k8sClient.Create(ctx, complete); err != nil {
		t.Fatalf("create nonmatching complete AgentRun: %v", err)
	}

	sparse := testAgentRun("sparse-through-webhook", map[string]any{
		"agentRef": map[string]any{"name": "task"},
	})
	err = k8sClient.Create(ctx, sparse)
	if !apierrors.IsInvalid(err) {
		t.Fatalf("sparse request should pass TLS admission then fail schema validation, got %v", err)
	}

	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("refresh registration: %v", err)
	}
	registration.Webhooks[0].ClientConfig.CABundle = []byte("untrusted")
	if err := k8sClient.Update(ctx, &registration); err != nil {
		t.Fatalf("break admission trust: %v", err)
	}
	untrustedSparse := testAgentRun("sparse-fails-closed", map[string]any{
		"agentRef": map[string]any{"name": "task"},
	})
	err = k8sClient.Create(ctx, untrustedSparse)
	if err == nil || apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "failed calling webhook") {
		t.Fatalf("matching request did not fail closed under broken TLS: %v", err)
	}
	unaffected := testAgentRun("complete-still-bypasses", completeSpec())
	if err := k8sClient.Create(ctx, unaffected); err != nil {
		t.Fatalf("nonmatching AgentRun depended on broken webhook TLS: %v", err)
	}

	cancelServer()
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatalf("webhook server stopped with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook server did not stop")
	}
}

func waitForWebhookServer(t *testing.T, server webhook.Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := server.StartedChecker()(nil); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("webhook server did not become ready")
}

func testAgentRun(name string, spec map[string]any) *unstructured.Unstructured {
	run := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kontext.dev/v1alpha1",
		"kind":       "AgentRun",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "default",
		},
		"spec": spec,
	}}
	run.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "kontext.dev", Version: "v1alpha1", Kind: "AgentRun",
	})
	return run
}

func completeSpec() map[string]any {
	return map[string]any{
		"goal":  "complete",
		"model": "test/model",
		"runtime": map[string]any{
			"image": "example.invalid/runtime:test",
		},
	}
}
