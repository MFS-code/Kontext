package controller

import (
	"testing"
	"time"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestBackoffDelaySaturatesForLargeRestartCount(t *testing.T) {
	reconciler := &AgentReconciler{}
	agent := kontextv1alpha1.Agent{
		Spec: kontextv1alpha1.AgentSpec{
			Backoff: &kontextv1alpha1.BackoffSpec{
				InitialSeconds: 1,
				MaxSeconds:     60,
			},
		},
	}

	if got := reconciler.backoffDelay(agent, maxRunSuffix-1); got != 60*time.Second {
		t.Fatalf("expected saturated 60-second backoff, got %s", got)
	}
}

func TestBackoffDelayCapsInitialAtMaximum(t *testing.T) {
	reconciler := &AgentReconciler{}
	agent := kontextv1alpha1.Agent{
		Spec: kontextv1alpha1.AgentSpec{
			Backoff: &kontextv1alpha1.BackoffSpec{
				InitialSeconds: 30,
				MaxSeconds:     10,
			},
		},
	}

	if got := reconciler.backoffDelay(agent, 1); got != 10*time.Second {
		t.Fatalf("expected maximum 10-second backoff, got %s", got)
	}
}
