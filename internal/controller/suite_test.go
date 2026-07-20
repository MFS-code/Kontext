package controller_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/controller"
)

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
	scheme    *runtime.Scheme
)

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
		},
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic(err)
	}

	scheme = runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kontextv1alpha1.AddToScheme(scheme))

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}

	code := m.Run()
	if stopErr := testEnv.Stop(); stopErr != nil {
		panic(stopErr)
	}
	os.Exit(code)
}

func newAgentRunReconciler() *controller.AgentRunReconciler {
	return &controller.AgentRunReconciler{
		Client:        k8sClient,
		Scheme:        scheme,
		ReporterImage: "kontext-reporter:dev",
	}
}

func newAgentReconciler() *controller.AgentReconciler {
	return &controller.AgentReconciler{
		Client:    k8sClient,
		APIReader: k8sClient,
		Scheme:    scheme,
	}
}

func reconcileAgentRun(ctx context.Context, t *testing.T, name types.NamespacedName) {
	t.Helper()
	_, err := newAgentRunReconciler().Reconcile(ctx, ctrl.Request{NamespacedName: name})
	if err != nil {
		t.Fatalf("reconcile agentrun: %v", err)
	}
}

func reconcileAgent(ctx context.Context, t *testing.T, name types.NamespacedName) {
	t.Helper()
	_, err := newAgentReconciler().Reconcile(ctx, ctrl.Request{NamespacedName: name})
	if err != nil {
		t.Fatalf("reconcile agent: %v", err)
	}
}

func echoRuntimeSpec() kontextv1alpha1.RuntimeSpec {
	return kontextv1alpha1.RuntimeSpec{Image: "kontext-echo:dev"}
}
