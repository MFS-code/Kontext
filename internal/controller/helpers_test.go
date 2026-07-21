package controller_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func updateAgentStatus(
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	next kontextv1alpha1.AgentStatus,
) error {
	agent.Status = next
	return k8sClient.Status().Update(ctx, agent)
}

func createOwnedAgentRun(
	ctx context.Context,
	t *testing.T,
	agent *kontextv1alpha1.Agent,
	run *kontextv1alpha1.AgentRun,
) {
	t.Helper()
	if err := controllerutil.SetControllerReference(agent, run, scheme); err != nil {
		t.Fatalf("set AgentRun owner: %v", err)
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
}

type staleAgentRunListClient struct {
	client.Client
	omitName types.NamespacedName
}

func (c *staleAgentRunListClient) List(
	ctx context.Context,
	list client.ObjectList,
	opts ...client.ListOption,
) error {
	runs, ok := list.(*kontextv1alpha1.AgentRunList)
	if !ok || c.omitName.Name == "" {
		return c.Client.List(ctx, list, opts...)
	}
	if err := c.Client.List(ctx, runs, opts...); err != nil {
		return err
	}
	filtered := runs.Items[:0]
	for i := range runs.Items {
		run := runs.Items[i]
		if run.Name == c.omitName.Name && run.Namespace == c.omitName.Namespace {
			continue
		}
		filtered = append(filtered, run)
	}
	runs.Items = filtered
	c.omitName = types.NamespacedName{}
	return nil
}

func testPtr[T any](value T) *T {
	return &value
}
