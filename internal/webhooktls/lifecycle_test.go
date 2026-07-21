package webhooktls

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	k8sClient := newFakeClient(t)
	options := testOptions(clock)
	options.CAValidity = 2 * time.Hour
	options.CARenewBefore = time.Hour
	lifecycle := NewLifecycle(k8sClient, &Store{}, options)
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("bootstrap lifecycle: %v", err)
	}
	original := getSecret(t, k8sClient)
	originalCA := append([]byte(nil), original.Data[CACertKey]...)

	clock.Advance(61 * time.Minute)
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("rotate CA: %v", err)
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

func TestPromotionRetriesWhenRegistrationChanges(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	k8sClient := newFakeClient(t)
	lifecycle := NewLifecycle(k8sClient, &Store{}, testOptions(clock))
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("bootstrap lifecycle: %v", err)
	}
	secret := getSecret(t, k8sClient)
	next, err := lifecycle.generate(secret.Data[CACertKey])
	if err != nil {
		t.Fatalf("generate staged certificates: %v", err)
	}
	if err := lifecycle.promoteNext(ctx, secret, next); !errors.Is(err, errRegistrationChanged) {
		t.Fatalf("registration race should be retryable, got %v", err)
	}
}

func TestServingRenewalPreservesStagedCARotation(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	k8sClient := newFakeClient(t)
	lifecycle := NewLifecycle(k8sClient, &Store{}, testOptions(clock))
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("bootstrap lifecycle: %v", err)
	}
	secret := getSecret(t, k8sClient)
	next, err := lifecycle.generate(secret.Data[CACertKey])
	if err != nil {
		t.Fatalf("generate staged certificates: %v", err)
	}
	if err := lifecycle.stageNext(ctx, secret, next); err != nil {
		t.Fatalf("stage CA rotation: %v", err)
	}

	clock.Advance(91 * time.Minute)
	if _, err := lifecycle.ensureSecret(ctx); err != nil {
		t.Fatalf("renew serving certificate with staged CA: %v", err)
	}
	renewed := getSecret(t, k8sClient)
	if !bytes.Equal(renewed.Data[nextCAKey], next.CACert) ||
		!bytes.Equal(renewed.Data[nextTLSKey], next.TLSCert) {
		t.Fatal("serving renewal discarded staged CA rotation")
	}
}

func TestTwoLifecycleReplicasConverge(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	k8sClient := newFakeClient(t)
	options := testOptions(clock)
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

func newFakeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := admissionv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add admission scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
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
