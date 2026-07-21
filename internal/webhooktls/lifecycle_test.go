package webhooktls

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

func TestLifecycleBootstrapReuseRepairAndRenewal(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	k8sClient := newFakeClient(t)
	options := testOptions(clock)
	firstStore := &Store{}
	first := NewLifecycle(k8sClient, firstStore, options)
	if err := first.Ensure(ctx); err != nil {
		t.Fatalf("bootstrap lifecycle: %v", err)
	}
	secret := getSecret(t, k8sClient)
	originalCA := append([]byte(nil), secret.Data[CACertKey]...)
	originalLeaf := append([]byte(nil), secret.Data[TLSCertKey]...)
	if err := first.ReadinessCheck(nil); err != nil {
		t.Fatalf("ready after bootstrap: %v", err)
	}

	secondStore := &Store{}
	second := NewLifecycle(k8sClient, secondStore, options)
	if err := second.Ensure(ctx); err != nil {
		t.Fatalf("reuse lifecycle: %v", err)
	}
	reused := getSecret(t, k8sClient)
	if !bytes.Equal(originalCA, reused.Data[CACertKey]) ||
		!bytes.Equal(originalLeaf, reused.Data[TLSCertKey]) {
		t.Fatal("restart did not reuse valid Secret material")
	}

	var registration admissionv1.MutatingWebhookConfiguration
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get webhook registration: %v", err)
	}
	registration.Annotations = map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": "release-manifest",
	}
	registration.Webhooks[0].ClientConfig.CABundle = []byte("stale")
	if err := k8sClient.Update(ctx, &registration); err != nil {
		t.Fatalf("damage CA bundle: %v", err)
	}
	if err := second.ReadinessCheck(nil); err == nil {
		t.Fatal("readiness remained healthy with stale API-server trust")
	}
	if err := second.Ensure(ctx); err != nil {
		t.Fatalf("repair CA bundle: %v", err)
	}
	if err := second.ReadinessCheck(nil); err != nil {
		t.Fatalf("ready after CA repair: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get repaired webhook registration: %v", err)
	}
	if registration.Annotations["kubectl.kubernetes.io/last-applied-configuration"] != "release-manifest" {
		t.Fatal("CA repair discarded declarative release metadata")
	}

	clock.Advance(91 * time.Minute)
	if err := second.Ensure(ctx); err != nil {
		t.Fatalf("renew near-expiry serving certificate: %v", err)
	}
	renewed := getSecret(t, k8sClient)
	if !bytes.Equal(originalCA, renewed.Data[CACertKey]) {
		t.Fatal("near-expiry serving renewal unexpectedly rotated CA")
	}
	if bytes.Equal(originalLeaf, renewed.Data[TLSCertKey]) {
		t.Fatal("near-expiry serving certificate was not renewed")
	}
}

func TestLifecycleRecoversInvalidSecretWithoutLeakingMaterial(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	k8sClient := newFakeClient(t, &corev1.Secret{
		ObjectMeta: objectMeta(DefaultNamespace, DefaultSecretName),
		Data: map[string][]byte{
			CACertKey:  []byte("invalid-ca"),
			CAKeyKey:   []byte("private-data-that-must-not-appear"),
			TLSCertKey: []byte("invalid-cert"),
			TLSKeyKey:  []byte("private-data-that-must-not-appear"),
		},
	})
	lifecycle := NewLifecycle(k8sClient, &Store{}, testOptions(clock))
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("recover invalid Secret: %v", err)
	}
	secret := getSecret(t, k8sClient)
	if bytes.Contains(secret.Data[TLSKeyKey], []byte("private-data-that-must-not-appear")) {
		t.Fatal("invalid private key was retained")
	}
	if _, err := parse(bundleFromSecret(secret), lifecycle.dnsNames(), clock.Now()); err != nil {
		t.Fatalf("recovered Secret is invalid: %v", err)
	}
}

func TestLifecycleCARotationPublishesOverlapBeforePromotion(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	baseClient := newFakeClient(t)
	k8sClient := &secretUpdateInterceptor{Client: baseClient}
	options := testOptions(clock)
	options.CAValidity = 2 * time.Hour
	options.CARenewBefore = time.Hour
	lifecycle := NewLifecycle(k8sClient, &Store{}, options)
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("bootstrap lifecycle: %v", err)
	}
	original := getSecret(t, k8sClient)
	originalCA := append([]byte(nil), original.Data[CACertKey]...)

	observedTrustBeforeSwap := false
	k8sClient.beforeUpdate = func(ctx context.Context, secret *corev1.Secret) error {
		var registration admissionv1.MutatingWebhookConfiguration
		if err := baseClient.Get(
			ctx,
			client.ObjectKey{Name: DefaultWebhookName},
			&registration,
		); err != nil {
			return err
		}
		if len(registration.Webhooks) == 1 &&
			bytes.Equal(registration.Webhooks[0].ClientConfig.CABundle, secret.Data[CACertKey]) {
			observedTrustBeforeSwap = true
		}
		return nil
	}
	clock.Advance(61 * time.Minute)
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("rotate CA: %v", err)
	}
	if !observedTrustBeforeSwap {
		t.Fatal("CA rotation swapped the Secret before publishing admission trust")
	}
	rotated := getSecret(t, k8sClient)
	if bytes.Equal(originalCA, rotated.Data[CACertKey]) {
		t.Fatal("near-expiry CA was not rotated")
	}
	if !bytes.Contains(rotated.Data[CACertKey], originalCA) {
		t.Fatal("CA rotation removed old trust before replicas could converge")
	}
	var registration admissionv1.MutatingWebhookConfiguration
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get webhook registration: %v", err)
	}
	if !bytes.Equal(registration.Webhooks[0].ClientConfig.CABundle, rotated.Data[CACertKey]) {
		t.Fatal("serving CA was promoted before admission trust")
	}
}

func TestLifecycleCARotationCrashBeforeSecretSwapPreservesTrust(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	baseClient := newFakeClient(t)
	k8sClient := &secretUpdateInterceptor{Client: baseClient}
	options := testOptions(clock)
	options.CAValidity = 2 * time.Hour
	options.CARenewBefore = time.Hour
	store := &Store{}
	lifecycle := NewLifecycle(k8sClient, store, options)
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("bootstrap lifecycle: %v", err)
	}
	original := getSecret(t, k8sClient)
	originalCA := append([]byte(nil), original.Data[CACertKey]...)
	originalTLS := append([]byte(nil), original.Data[TLSCertKey]...)

	swapErr := errors.New("injected crash before Secret swap")
	k8sClient.beforeUpdate = func(context.Context, *corev1.Secret) error {
		return swapErr
	}
	clock.Advance(61 * time.Minute)
	if err := lifecycle.Ensure(ctx); !errors.Is(err, swapErr) {
		t.Fatalf("expected injected Secret swap failure, got %v", err)
	}

	unchanged := getSecret(t, k8sClient)
	if !bytes.Equal(unchanged.Data[CACertKey], originalCA) ||
		!bytes.Equal(unchanged.Data[TLSCertKey], originalTLS) {
		t.Fatal("failed Secret swap changed the active serving material")
	}
	var registration admissionv1.MutatingWebhookConfiguration
	if err := baseClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get webhook registration after failed swap: %v", err)
	}
	publishedTrust := registration.Webhooks[0].ClientConfig.CABundle
	if bytes.Equal(publishedTrust, originalCA) || !bytes.Contains(publishedTrust, originalCA) {
		t.Fatal("registration did not retain old trust while publishing the replacement CA")
	}
	parsed, err := parse(bundleFromSecret(unchanged), lifecycle.dnsNames(), clock.Now())
	if err != nil {
		t.Fatalf("parse unchanged serving material: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(publishedTrust) {
		t.Fatal("published registration trust is not valid PEM")
	}
	if _, err := parsed.leaf.Verify(x509.VerifyOptions{
		DNSName:     lifecycle.dnsNames()[2],
		Roots:       roots,
		CurrentTime: clock.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("old serving certificate stopped working before Secret swap: %v", err)
	}

	k8sClient.beforeUpdate = nil
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("recover rotation after failed Secret swap: %v", err)
	}
	if err := lifecycle.ReadinessCheck(nil); err != nil {
		t.Fatalf("webhook did not converge after rotation retry: %v", err)
	}
}

func TestDesiredRegistrationMatchesManagerManifest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..", "..", "config", "manager", "mutating_webhook_configuration.yaml",
	))
	if err != nil {
		t.Fatalf("read webhook registration manifest: %v", err)
	}
	var manifest admissionv1.MutatingWebhookConfiguration
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse webhook registration manifest: %v", err)
	}

	// config/default applies this prefix to resource names and service references.
	const namePrefix = "kontext-"
	manifest.Name = namePrefix + manifest.Name
	for index := range manifest.Webhooks {
		service := manifest.Webhooks[index].ClientConfig.Service
		if service != nil {
			service.Name = namePrefix + service.Name
		}
	}

	lifecycle := NewLifecycle(nil, nil, DefaultOptions())
	desired := lifecycle.desiredRegistration(nil)
	if !reflect.DeepEqual(manifest, *desired) {
		t.Fatalf(
			"manager manifest and runtime webhook registration differ:\nmanifest: %#v\ndesired:  %#v",
			manifest,
			*desired,
		)
	}
}

func TestTwoLifecycleReplicasConverge(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	k8sClient := newFakeClient(t)
	options := testOptions(clock)
	options.ServingValidity = 72 * time.Hour
	stores := []*Store{{}, {}}
	lifecycles := []*Lifecycle{
		NewLifecycle(k8sClient, stores[0], options),
		NewLifecycle(k8sClient, stores[1], options),
	}

	var wait sync.WaitGroup
	results := make(chan error, len(lifecycles))
	for _, lifecycle := range lifecycles {
		wait.Add(1)
		go func(current *Lifecycle) {
			defer wait.Done()
			results <- current.Ensure(ctx)
		}(lifecycle)
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent bootstrap: %v", err)
		}
	}
	if !bytes.Equal(stores[0].CABundle(), stores[1].CABundle()) {
		t.Fatal("replicas did not converge on shared CA")
	}
	first, err := stores[0].GetCertificate(nil)
	if err != nil {
		t.Fatalf("first replica certificate: %v", err)
	}
	second, err := stores[1].GetCertificate(nil)
	if err != nil {
		t.Fatalf("second replica certificate: %v", err)
	}
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("replicas did not converge on shared serving certificate")
	}

	clock.Advance(47*time.Hour + time.Minute)
	results = make(chan error, len(lifecycles))
	for _, lifecycle := range lifecycles {
		wait.Add(1)
		go func(current *Lifecycle) {
			defer wait.Done()
			results <- current.Ensure(ctx)
		}(lifecycle)
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent CA rotation: %v", err)
		}
	}
	if !bytes.Equal(stores[0].CABundle(), stores[1].CABundle()) {
		t.Fatal("replicas did not converge after concurrent CA rotation")
	}
	rotated := getSecret(t, k8sClient)
	var registration admissionv1.MutatingWebhookConfiguration
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get registration after concurrent rotation: %v", err)
	}
	if !bytes.Equal(registration.Webhooks[0].ClientConfig.CABundle, rotated.Data[CACertKey]) {
		t.Fatal("concurrent CA rotation left serving material and admission trust mismatched")
	}
}

func testOptions(clock *testClock) Options {
	options := DefaultOptions()
	options.Clock = clock.Now
	options.CAValidity = 48 * time.Hour
	options.CARenewBefore = time.Hour
	options.ServingValidity = 2 * time.Hour
	options.ServingRenewBefore = 30 * time.Minute
	options.Backdate = time.Minute
	return options
}

type secretUpdateInterceptor struct {
	client.Client
	beforeUpdate func(context.Context, *corev1.Secret) error
}

func (c *secretUpdateInterceptor) Update(
	ctx context.Context,
	object client.Object,
	options ...client.UpdateOption,
) error {
	if secret, ok := object.(*corev1.Secret); ok && c.beforeUpdate != nil {
		if err := c.beforeUpdate(ctx, secret); err != nil {
			return err
		}
	}
	return c.Client.Update(ctx, object, options...)
}

func newFakeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := admissionv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add admission scheme: %v", err)
	}
	var applyMu sync.Mutex
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(
				ctx context.Context,
				k8sClient client.WithWatch,
				obj client.Object,
				patch client.Patch,
				_ ...client.PatchOption,
			) error {
				if patch.Type() != types.ApplyPatchType {
					return k8sClient.Patch(ctx, obj, patch)
				}
				desired, ok := obj.(*admissionv1.MutatingWebhookConfiguration)
				if !ok {
					t.Fatalf("unexpected apply object type %T", obj)
				}

				applyMu.Lock()
				defer applyMu.Unlock()
				var existing admissionv1.MutatingWebhookConfiguration
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
				if apierrors.IsNotFound(err) {
					return k8sClient.Create(ctx, desired.DeepCopy())
				}
				if err != nil {
					return err
				}
				existing.TypeMeta = desired.TypeMeta
				if existing.Labels == nil {
					existing.Labels = map[string]string{}
				}
				for key, value := range desired.Labels {
					existing.Labels[key] = value
				}
				existing.Webhooks = desired.DeepCopy().Webhooks
				return k8sClient.Update(ctx, &existing)
			},
		}).
		Build()
}

func getSecret(t *testing.T, k8sClient client.Client) *corev1.Secret {
	t.Helper()
	var secret corev1.Secret
	if err := k8sClient.Get(context.Background(), client.ObjectKey{
		Namespace: DefaultNamespace,
		Name:      DefaultSecretName,
	}, &secret); err != nil {
		t.Fatalf("get webhook Secret: %v", err)
	}
	return &secret
}

func objectMeta(namespace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namespace, Name: name}
}
