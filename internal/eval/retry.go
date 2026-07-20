package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

type kubernetesErrorClassification struct {
	retryableRead  bool
	ambiguousWrite bool
}

func classifyKubernetesError(err error) kubernetesErrorClassification {
	if apierrors.IsAlreadyExists(err) ||
		apierrors.IsBadRequest(err) ||
		apierrors.IsInvalid(err) ||
		apierrors.IsForbidden(err) ||
		apierrors.IsUnauthorized(err) ||
		apierrors.IsMethodNotSupported(err) ||
		apierrors.IsNotAcceptable(err) ||
		apierrors.IsUnsupportedMediaType(err) {
		return kubernetesErrorClassification{}
	}
	if apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return kubernetesErrorClassification{retryableRead: true, ambiguousWrite: true}
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return kubernetesErrorClassification{retryableRead: true, ambiguousWrite: true}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return kubernetesErrorClassification{retryableRead: true, ambiguousWrite: true}
	}
	return kubernetesErrorClassification{ambiguousWrite: true}
}

func (runner Runner) cleanupAmbiguousCreate(
	parent context.Context,
	key types.NamespacedName,
	expectedLabels map[string]string,
) []string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 300*time.Millisecond)
	defer cancel()
	var lastErr error
	deleteAttempted := false
	err := clientretry.OnError(wait.Backoff{
		Duration: 25 * time.Millisecond,
		Factor:   2,
		Steps:    8,
		Cap:      200 * time.Millisecond,
	}, func(err error) bool {
		return ctx.Err() == nil &&
			(apierrors.IsNotFound(err) || classifyKubernetesError(err).retryableRead)
	}, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		observed := &kontextv1alpha1.AgentRun{}
		err := runner.Client.Get(ctx, key, observed)
		if apierrors.IsNotFound(err) && deleteAttempted {
			return nil
		}
		if err == nil {
			if !hasExactEvaluatorOwnership(observed.Labels, expectedLabels) {
				return errors.New("ambiguous AgentRun exists without exact evaluator ownership; left untouched")
			}
			deleteAttempted = true
			if err := runner.Client.Delete(ctx, observed); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("cleanup ambiguous AgentRun: %w", err)
			}
			return nil
		}
		lastErr = err
		return err
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return []string{fmt.Sprintf(
			"probe ambiguous AgentRun after create error: %v (last read: %v)",
			ctx.Err(),
			lastErr,
		)}
	}
	return []string{err.Error()}
}

func hasExactEvaluatorOwnership(actual, expected map[string]string) bool {
	for _, name := range []string{
		labelManagedBy,
		labelEvalSuite,
		labelEvalCase,
		labelInvocation,
	} {
		if actual[name] == "" || actual[name] != expected[name] {
			return false
		}
	}
	return true
}

func (runner Runner) getWithRetry(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
) error {
	return clientretry.OnError(wait.Backoff{
		Duration: 25 * time.Millisecond,
		Factor:   2,
		Steps:    10_000,
		Cap:      500 * time.Millisecond,
	}, func(err error) bool {
		return ctx.Err() == nil && classifyKubernetesError(err).retryableRead
	}, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return runner.Client.Get(ctx, key, object)
	})
}
