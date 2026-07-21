package controller_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	testEnv       *envtest.Environment
	cfg           *rest.Config
	k8sClient     client.Client
	apiReader     client.Reader
	indexedReader client.Reader
	scheme        *runtime.Scheme
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

	manager, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	ctx, cancelCache := context.WithCancel(context.Background())
	if err := controller.RegisterAgentRunOwnerIndex(ctx, manager.GetFieldIndexer()); err != nil {
		panic(err)
	}
	cacheErrors := make(chan error, 1)
	go func() {
		cacheErrors <- manager.GetCache().Start(ctx)
	}()
	syncCtx, cancelSync := context.WithTimeout(ctx, 10*time.Second)
	cacheSynced := manager.GetCache().WaitForCacheSync(syncCtx)
	cancelSync()
	if !cacheSynced {
		cancelCache()
		panic("controller test cache did not sync")
	}
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	apiReader = manager.GetAPIReader()
	indexedReader = manager.GetClient()

	code := m.Run()
	cancelCache()
	if cacheErr := <-cacheErrors; cacheErr != nil {
		panic(cacheErr)
	}
	if stopErr := testEnv.Stop(); stopErr != nil {
		panic(stopErr)
	}
	os.Exit(code)
}

func newAgentRunReconciler() *controller.AgentRunReconciler {
	return &controller.AgentRunReconciler{
		Client:        k8sClient,
		APIReader:     apiReader,
		Scheme:        scheme,
		ReporterImage: "kontext-reporter:dev",
	}
}

func newAgentReconciler() *controller.AgentReconciler {
	return &controller.AgentReconciler{
		Client:    newOwnerIndexedClient(),
		APIReader: apiReader,
		Scheme:    scheme,
	}
}

type ownerIndexedClient struct {
	client.Client
	indexedReader client.Reader
}

func newOwnerIndexedClient() client.Client {
	return &ownerIndexedClient{Client: k8sClient, indexedReader: indexedReader}
}

func (c *ownerIndexedClient) List(
	ctx context.Context,
	list client.ObjectList,
	opts ...client.ListOption,
) error {
	runs, isAgentRunList := list.(*kontextv1alpha1.AgentRunList)
	options := (&client.ListOptions{}).ApplyOptions(opts)
	if !isAgentRunList || options.FieldSelector == nil {
		return c.Client.List(ctx, list, opts...)
	}
	ownerUID, indexed := options.FieldSelector.RequiresExactMatch(controller.AgentRunOwnerUIDField)
	if !indexed {
		return c.Client.List(ctx, list, opts...)
	}

	var liveRuns kontextv1alpha1.AgentRunList
	if err := c.Client.List(
		ctx,
		&liveRuns,
		client.InNamespace(options.Namespace),
	); err != nil {
		return err
	}
	expected := make(map[string]string)
	for i := range liveRuns.Items {
		owner := metav1.GetControllerOf(&liveRuns.Items[i])
		if owner != nil &&
			owner.APIVersion == kontextv1alpha1.GroupVersion.String() &&
			owner.Kind == "Agent" &&
			string(owner.UID) == ownerUID {
			expected[liveRuns.Items[i].Name] = liveRuns.Items[i].ResourceVersion
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := c.indexedReader.List(ctx, runs, opts...); err != nil {
			return err
		}
		if indexedRunsMatch(runs.Items, expected) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("AgentRun owner index did not converge for UID %s", ownerUID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func indexedRunsMatch(
	actual []kontextv1alpha1.AgentRun,
	expected map[string]string,
) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		resourceVersion, ok := expected[actual[i].Name]
		if !ok || actual[i].ResourceVersion != resourceVersion {
			return false
		}
	}
	return true
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
