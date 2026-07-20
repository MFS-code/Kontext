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
	retryableRead bool
}

func classifyKubernetesError(err error) kubernetesErrorClassification {
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
		return kubernetesErrorClassification{retryableRead: true}
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return kubernetesErrorClassification{retryableRead: true}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return kubernetesErrorClassification{retryableRead: true}
	}
	return kubernetesErrorClassification{}
}

func (runner Runner) cleanupCreateFailure(
	parent context.Context,
	key types.NamespacedName,
	expectedLabels map[string]string,
) []string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 300*time.Millisecond)
	defer cancel()
	observed := &kontextv1alpha1.AgentRun{}
	// A failed single probe cannot establish ownership, so leave the run untouched.
	if err := runner.Client.Get(ctx, key, observed); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return []string{fmt.Sprintf("probe AgentRun after create error: %v", err)}
	}
	if !hasExactEvaluatorOwnership(observed.Labels, expectedLabels) {
		return []string{"AgentRun exists without exact evaluator ownership; left untouched"}
	}
	if err := runner.Client.Delete(ctx, observed); err != nil && !apierrors.IsNotFound(err) {
		return []string{fmt.Sprintf("cleanup AgentRun after create error: %v", err)}
	}
	return nil
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
