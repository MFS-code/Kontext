package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	deliveryv1alpha1 "github.com/MFS-code/Kontext/pkg/delivery/v1alpha1"
)

const (
	deliveryCredentialBytes  = 32
	deliverySecretNamePrefix = "delivery-auth-"
)

func (r *AgentRunReconciler) ensureDeliveryCredential(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
) (string, error) {
	if r.APIReader == nil {
		return "", fmt.Errorf(
			"cannot ensure delivery credential for AgentRun %s/%s: APIReader is not configured",
			run.Namespace,
			run.Name,
		)
	}
	secretName, err := deliveryCredentialSecretName(run)
	if err != nil {
		return "", err
	}

	var existing corev1.Secret
	key := client.ObjectKey{Namespace: run.Namespace, Name: secretName}
	if err := r.APIReader.Get(ctx, key, &existing); err == nil {
		if _, err := deliveryCredentialFromSecret(run, &existing); err != nil {
			return "", err
		}
		return secretName, nil
	} else if !apierrors.IsNotFound(err) {
		return "", err
	}

	tokenBytes := make([]byte, deliveryCredentialBytes)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate delivery credential: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	immutable := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: run.Namespace,
			Labels: map[string]string{
				podbuilder.LabelRunName: run.Name,
			},
		},
		Immutable: &immutable,
		Type:      corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			deliveryv1alpha1.TokenSecretKey: []byte(token),
		},
	}
	if err := controllerutil.SetControllerReference(run, secret, r.Scheme); err != nil {
		return "", err
	}
	if err := r.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", err
		}
		if err := r.APIReader.Get(ctx, key, &existing); err != nil {
			return "", err
		}
		if _, err := deliveryCredentialFromSecret(run, &existing); err != nil {
			return "", err
		}
	}
	return secretName, nil
}

func (r *AgentRunReconciler) loadDeliveryCredential(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
) ([]byte, error) {
	if r.APIReader == nil {
		return nil, fmt.Errorf(
			"cannot load delivery credential for AgentRun %s/%s: APIReader is not configured",
			run.Namespace,
			run.Name,
		)
	}
	secretName, err := deliveryCredentialSecretName(run)
	if err != nil {
		return nil, err
	}
	var secret corev1.Secret
	if err := r.APIReader.Get(ctx, client.ObjectKey{
		Namespace: run.Namespace,
		Name:      secretName,
	}, &secret); err != nil {
		return nil, err
	}
	return deliveryCredentialFromSecret(run, &secret)
}

func deliveryCredentialSecretName(run *kontextv1alpha1.AgentRun) (string, error) {
	if run == nil || run.UID == "" {
		return "", fmt.Errorf("delivery credential requires a persisted AgentRun UID")
	}
	digest := sha256.Sum256([]byte(run.UID))
	return deliverySecretNamePrefix + hex.EncodeToString(digest[:8]), nil
}

func deliveryCredentialFromSecret(
	run *kontextv1alpha1.AgentRun,
	secret *corev1.Secret,
) ([]byte, error) {
	if !metav1.IsControlledBy(secret, run) {
		return nil, fmt.Errorf(
			"delivery credential Secret %s/%s is not controlled by AgentRun %s/%s",
			secret.Namespace,
			secret.Name,
			run.Namespace,
			run.Name,
		)
	}
	token := secret.Data[deliveryv1alpha1.TokenSecretKey]
	if len(token) == 0 {
		return nil, fmt.Errorf(
			"delivery credential Secret %s/%s has no %q data",
			secret.Namespace,
			secret.Name,
			deliveryv1alpha1.TokenSecretKey,
		)
	}
	return append([]byte(nil), token...), nil
}
