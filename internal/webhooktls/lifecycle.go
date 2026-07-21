package webhooktls

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	DefaultNamespace         = "kontext-system"
	DefaultSecretName        = "webhook-server-cert"
	DefaultWebhookName       = "kontext-task-agentrun-mutator.kontext.dev"
	DefaultServiceName       = "kontext-webhook-service"
	DefaultWebhookPath       = "/mutate-kontext-dev-v1alpha1-agentrun"
	DefaultReconcileInterval = 30 * time.Second

	nextCAKey     = "next-ca.crt"
	nextCAKeyPEM  = "next-ca.key"
	nextTLSKey    = "next-tls.crt"
	nextTLSKeyPEM = "next-tls.key"
)

var errRegistrationChanged = errors.New("webhook registration changed during CA promotion")

type Clock func() time.Time

type Options struct {
	Namespace          string
	SecretName         string
	WebhookName        string
	ServiceName        string
	WebhookPath        string
	ReconcileInterval  time.Duration
	CAValidity         time.Duration
	ServingValidity    time.Duration
	CARenewBefore      time.Duration
	ServingRenewBefore time.Duration
	Backdate           time.Duration
	Clock              Clock
}

func DefaultOptions() Options {
	return Options{
		Namespace:          DefaultNamespace,
		SecretName:         DefaultSecretName,
		WebhookName:        DefaultWebhookName,
		ServiceName:        DefaultServiceName,
		WebhookPath:        DefaultWebhookPath,
		ReconcileInterval:  DefaultReconcileInterval,
		CAValidity:         10 * 365 * 24 * time.Hour,
		ServingValidity:    365 * 24 * time.Hour,
		CARenewBefore:      365 * 24 * time.Hour,
		ServingRenewBefore: 30 * 24 * time.Hour,
		Backdate:           5 * time.Minute,
		Clock:              time.Now,
	}
}

type Lifecycle struct {
	client client.Client
	store  *Store
	opts   Options
}

func NewLifecycle(k8sClient client.Client, store *Store, options Options) *Lifecycle {
	return &Lifecycle{client: k8sClient, store: store, opts: options}
}

func (l *Lifecycle) Start(ctx context.Context) error {
	if len(l.store.CABundle()) == 0 {
		if err := l.Ensure(ctx); err != nil {
			return fmt.Errorf("initialize webhook TLS: %w", err)
		}
	}
	ticker := time.NewTicker(l.opts.ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := l.Ensure(ctx); err != nil {
				log.FromContext(ctx).Error(err, "webhook TLS reconciliation failed")
			}
		}
	}
}

func (l *Lifecycle) NeedLeaderElection() bool {
	return false
}

func (l *Lifecycle) Ensure(ctx context.Context) error {
	for attempts := 0; attempts < 5; attempts++ {
		secret, err := l.ensureSecret(ctx)
		if err != nil {
			return err
		}
		current := bundleFromSecret(secret)
		parsed, err := parse(current, l.dnsNames(), l.opts.Clock())
		if err != nil {
			return fmt.Errorf("validate reconciled webhook Secret: %w", err)
		}

		next := nextBundleFromSecret(secret)
		if len(next.CACert) > 0 {
			nextParsed, err := parse(next, l.dnsNames(), l.opts.Clock())
			if err != nil {
				if err := l.clearNext(ctx, secret); err != nil {
					if apierrors.IsConflict(err) {
						continue
					}
					return err
				}
				continue
			}
			if err := l.reconcileRegistration(ctx, next.CACert); err != nil {
				return err
			}
			if err := l.promoteNext(ctx, secret, next); err != nil {
				if apierrors.IsConflict(err) || errors.Is(err, errRegistrationChanged) {
					continue
				}
				return err
			}
			l.store.load(nextParsed)
			return nil
		}

		if parsed.ca.NotAfter.Sub(l.opts.Clock()) <= l.opts.CARenewBefore {
			next, err := Generate(CertificateOptions{
				DNSNames:         l.dnsNames(),
				Now:              l.opts.Clock(),
				CAValidity:       l.opts.CAValidity,
				ServingValidity:  l.opts.ServingValidity,
				Backdate:         l.opts.Backdate,
				PreviousCABundle: current.CACert,
			})
			if err != nil {
				return err
			}
			if err := l.stageNext(ctx, secret, next); err != nil {
				if apierrors.IsConflict(err) {
					continue
				}
				return err
			}
			continue
		}

		if err := l.reconcileRegistration(ctx, current.CACert); err != nil {
			return err
		}
		l.store.load(parsed)
		return nil
	}
	return fmt.Errorf("webhook TLS reconciliation did not converge after conflicts")
}

func (l *Lifecycle) ensureSecret(ctx context.Context) (*corev1.Secret, error) {
	var result *corev1.Secret
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var secret corev1.Secret
		key := types.NamespacedName{Namespace: l.opts.Namespace, Name: l.opts.SecretName}
		err := l.client.Get(ctx, key, &secret)
		if apierrors.IsNotFound(err) {
			previousCA, caErr := l.registeredCABundle(ctx)
			if caErr != nil {
				return caErr
			}
			generated, generateErr := l.generate(previousCA)
			if generateErr != nil {
				return generateErr
			}
			if reconcileErr := l.reconcileRegistration(ctx, generated.CACert); reconcileErr != nil {
				return reconcileErr
			}
			created := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: l.opts.Namespace,
					Name:      l.opts.SecretName,
					Labels:    managedLabels(),
				},
				Type: corev1.SecretTypeTLS,
				Data: secretData(generated),
			}
			if createErr := l.client.Create(ctx, created); createErr != nil {
				if apierrors.IsAlreadyExists(createErr) {
					return apierrors.NewConflict(corev1.Resource("secrets"), l.opts.SecretName, createErr)
				}
				return createErr
			}
			result = created
			return nil
		}
		if err != nil {
			return err
		}

		current := bundleFromSecret(&secret)
		parsed, parseErr := parse(current, l.dnsNames(), l.opts.Clock())
		if parseErr != nil {
			previousCA, caErr := l.registeredCABundle(ctx)
			if caErr != nil {
				return caErr
			}
			generated, generateErr := l.generate(previousCA)
			if generateErr != nil {
				return generateErr
			}
			if reconcileErr := l.reconcileRegistration(ctx, generated.CACert); reconcileErr != nil {
				return reconcileErr
			}
			secret.Type = corev1.SecretTypeTLS
			secret.Labels = managedLabels()
			secret.Data = secretData(generated)
			if updateErr := l.client.Update(ctx, &secret); updateErr != nil {
				return updateErr
			}
			result = secret.DeepCopy()
			return nil
		}

		if parsed.leaf.NotAfter.Sub(l.opts.Clock()) <= l.opts.ServingRenewBefore {
			renewed, renewErr := RenewServing(current, CertificateOptions{
				DNSNames:        l.dnsNames(),
				Now:             l.opts.Clock(),
				ServingValidity: l.opts.ServingValidity,
				Backdate:        l.opts.Backdate,
			})
			if renewErr != nil {
				return renewErr
			}
			if secret.Data == nil {
				secret.Data = map[string][]byte{}
			}
			for key, value := range secretData(renewed) {
				secret.Data[key] = value
			}
			if updateErr := l.client.Update(ctx, &secret); updateErr != nil {
				return updateErr
			}
		}
		result = secret.DeepCopy()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile webhook TLS Secret: %w", err)
	}
	return result, nil
}

func (l *Lifecycle) generate(previousCA []byte) (Bundle, error) {
	return Generate(CertificateOptions{
		DNSNames:         l.dnsNames(),
		Now:              l.opts.Clock(),
		CAValidity:       l.opts.CAValidity,
		ServingValidity:  l.opts.ServingValidity,
		Backdate:         l.opts.Backdate,
		PreviousCABundle: previousCA,
	})
}

func (l *Lifecycle) registeredCABundle(ctx context.Context) ([]byte, error) {
	var registration admissionv1.MutatingWebhookConfiguration
	err := l.client.Get(ctx, client.ObjectKey{Name: l.opts.WebhookName}, &registration)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read existing webhook CA trust: %w", err)
	}
	if len(registration.Webhooks) != 1 {
		return nil, nil
	}
	return append([]byte(nil), registration.Webhooks[0].ClientConfig.CABundle...), nil
}

func (l *Lifecycle) stageNext(ctx context.Context, secret *corev1.Secret, next Bundle) error {
	updated := secret.DeepCopy()
	if updated.Data == nil {
		updated.Data = map[string][]byte{}
	}
	updated.Data[nextCAKey] = next.CACert
	updated.Data[nextCAKeyPEM] = next.CAKey
	updated.Data[nextTLSKey] = next.TLSCert
	updated.Data[nextTLSKeyPEM] = next.TLSKey
	return l.client.Update(ctx, updated)
}

func (l *Lifecycle) promoteNext(ctx context.Context, secret *corev1.Secret, next Bundle) error {
	var registration admissionv1.MutatingWebhookConfiguration
	if err := l.client.Get(ctx, client.ObjectKey{Name: l.opts.WebhookName}, &registration); err != nil {
		return fmt.Errorf("verify webhook registration before CA promotion: %w", err)
	}
	if len(registration.Webhooks) != 1 || !bytes.Equal(registration.Webhooks[0].ClientConfig.CABundle, next.CACert) {
		return errRegistrationChanged
	}
	updated := secret.DeepCopy()
	updated.Data = secretData(next)
	return l.client.Update(ctx, updated)
}

func (l *Lifecycle) clearNext(ctx context.Context, secret *corev1.Secret) error {
	updated := secret.DeepCopy()
	delete(updated.Data, nextCAKey)
	delete(updated.Data, nextCAKeyPEM)
	delete(updated.Data, nextTLSKey)
	delete(updated.Data, nextTLSKeyPEM)
	return l.client.Update(ctx, updated)
}

func (l *Lifecycle) reconcileRegistration(ctx context.Context, caBundle []byte) error {
	desired := l.desiredRegistration(caBundle)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var existing admissionv1.MutatingWebhookConfiguration
		err := l.client.Get(ctx, client.ObjectKey{Name: l.opts.WebhookName}, &existing)
		if apierrors.IsNotFound(err) {
			if createErr := l.client.Create(ctx, desired.DeepCopy()); apierrors.IsAlreadyExists(createErr) {
				return apierrors.NewConflict(
					admissionv1.Resource("mutatingwebhookconfigurations"),
					l.opts.WebhookName,
					createErr,
				)
			} else {
				return createErr
			}
		}
		if err != nil {
			return err
		}
		if reflect.DeepEqual(existing.Webhooks, desired.Webhooks) &&
			existing.Labels["app.kubernetes.io/name"] == "kontext" &&
			existing.Labels["app.kubernetes.io/managed-by"] == "kontext" {
			return nil
		}
		desired.ObjectMeta = *existing.ObjectMeta.DeepCopy()
		if desired.Labels == nil {
			desired.Labels = map[string]string{}
		}
		for key, value := range managedLabels() {
			desired.Labels[key] = value
		}
		return l.client.Update(ctx, desired.DeepCopy())
	})
}

func (l *Lifecycle) desiredRegistration(caBundle []byte) *admissionv1.MutatingWebhookConfiguration {
	path := l.opts.WebhookPath
	port := int32(443)
	fail := admissionv1.Fail
	none := admissionv1.SideEffectClassNone
	equivalent := admissionv1.Equivalent
	never := admissionv1.NeverReinvocationPolicy
	timeout := int32(5)
	return &admissionv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: l.opts.WebhookName, Labels: managedLabels()},
		Webhooks: []admissionv1.MutatingWebhook{{
			Name: l.opts.WebhookName,
			ClientConfig: admissionv1.WebhookClientConfig{
				Service: &admissionv1.ServiceReference{
					Namespace: l.opts.Namespace,
					Name:      l.opts.ServiceName,
					Path:      &path,
					Port:      &port,
				},
				CABundle: append([]byte(nil), caBundle...),
			},
			Rules: []admissionv1.RuleWithOperations{{
				Operations: []admissionv1.OperationType{admissionv1.Create},
				Rule: admissionv1.Rule{
					APIGroups:   []string{"kontext.dev"},
					APIVersions: []string{"v1alpha1"},
					Resources:   []string{"agentruns"},
					Scope:       ptr(admissionv1.NamespacedScope),
				},
			}},
			FailurePolicy:           &fail,
			MatchPolicy:             &equivalent,
			SideEffects:             &none,
			TimeoutSeconds:          &timeout,
			AdmissionReviewVersions: []string{"v1"},
			ReinvocationPolicy:      &never,
			MatchConditions: []admissionv1.MatchCondition{{
				Name: "sparse-referenced-agent-run",
				Expression: "has(object.spec.agentRef) && " +
					"(!has(object.spec.goal) || object.spec.goal == '' || " +
					"!has(object.spec.model) || object.spec.model == '' || " +
					"!has(object.spec.runtime) || !has(object.spec.runtime.image) || object.spec.runtime.image == '')",
			}},
		}},
	}
}

func (l *Lifecycle) ReadinessCheck(_ *http.Request) error {
	caBundle := l.store.CABundle()
	if len(caBundle) == 0 {
		return fmt.Errorf("webhook serving certificate is not initialized")
	}
	certificate, err := l.store.GetCertificate(nil)
	if err != nil {
		return err
	}
	leaf := certificate.Leaf
	if leaf == nil && len(certificate.Certificate) > 0 {
		leaf, err = x509.ParseCertificate(certificate.Certificate[0])
	}
	roots := x509.NewCertPool()
	if leaf == nil || err != nil || !roots.AppendCertsFromPEM(caBundle) {
		return fmt.Errorf("webhook serving certificate is invalid")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:     fmt.Sprintf("%s.%s.svc", l.opts.ServiceName, l.opts.Namespace),
		Roots:       roots,
		CurrentTime: l.opts.Clock(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("webhook serving certificate is not trusted: %w", err)
	}
	var registration admissionv1.MutatingWebhookConfiguration
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := l.client.Get(ctx, client.ObjectKey{Name: l.opts.WebhookName}, &registration); err != nil {
		return fmt.Errorf("read webhook registration: %w", err)
	}
	if len(registration.Webhooks) != 1 ||
		!bytes.Equal(registration.Webhooks[0].ClientConfig.CABundle, caBundle) {
		return fmt.Errorf("webhook serving certificate and API server trust do not agree")
	}
	return nil
}

func (l *Lifecycle) dnsNames() []string {
	return []string{
		l.opts.ServiceName,
		fmt.Sprintf("%s.%s", l.opts.ServiceName, l.opts.Namespace),
		fmt.Sprintf("%s.%s.svc", l.opts.ServiceName, l.opts.Namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", l.opts.ServiceName, l.opts.Namespace),
	}
}

func managedLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "kontext",
		"app.kubernetes.io/managed-by": "kontext",
	}
}

func secretData(bundle Bundle) map[string][]byte {
	return map[string][]byte{
		CACertKey:  append([]byte(nil), bundle.CACert...),
		CAKeyKey:   append([]byte(nil), bundle.CAKey...),
		TLSCertKey: append([]byte(nil), bundle.TLSCert...),
		TLSKeyKey:  append([]byte(nil), bundle.TLSKey...),
	}
}

func bundleFromSecret(secret *corev1.Secret) Bundle {
	return Bundle{
		CACert:  secret.Data[CACertKey],
		CAKey:   secret.Data[CAKeyKey],
		TLSCert: secret.Data[TLSCertKey],
		TLSKey:  secret.Data[TLSKeyKey],
	}
}

func nextBundleFromSecret(secret *corev1.Secret) Bundle {
	return Bundle{
		CACert:  secret.Data[nextCAKey],
		CAKey:   secret.Data[nextCAKeyPEM],
		TLSCert: secret.Data[nextTLSKey],
		TLSKey:  secret.Data[nextTLSKeyPEM],
	}
}

func TLSOption(store *Store) func(*tls.Config) {
	return func(config *tls.Config) {
		config.GetCertificate = store.GetCertificate
	}
}

func ptr[T any](value T) *T {
	return &value
}
