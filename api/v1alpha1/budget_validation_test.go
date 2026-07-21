package v1alpha1_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestBudgetMinimumAdmissionValidation(t *testing.T) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
		},
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	scheme := runtime.NewScheme()
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register Kontext API types: %v", err)
	}
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("create envtest client: %v", err)
	}

	tests := []struct {
		name      string
		budget    *kontextv1alpha1.BudgetSpec
		wantValid bool
	}{
		{name: "minimum tokens", budget: &kontextv1alpha1.BudgetSpec{Tokens: int32Ptr(1)}, wantValid: true},
		{name: "zero tokens", budget: &kontextv1alpha1.BudgetSpec{Tokens: int32Ptr(0)}},
		{name: "negative tokens", budget: &kontextv1alpha1.BudgetSpec{Tokens: int32Ptr(-1)}},
		{name: "zero dollars", budget: &kontextv1alpha1.BudgetSpec{Dollars: float64Ptr(0)}, wantValid: true},
		{name: "negative dollars", budget: &kontextv1alpha1.BudgetSpec{Dollars: float64Ptr(-0.01)}},
	}
	objectFactories := map[string]func(string, *kontextv1alpha1.BudgetSpec) client.Object{
		"Agent": func(name string, budget *kontextv1alpha1.BudgetSpec) client.Object {
			return &kontextv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: kontextv1alpha1.AgentSpec{
					Mode:    kontextv1alpha1.AgentModeService,
					Goal:    "test budget validation",
					Model:   "test/model",
					Budget:  budget,
					Runtime: kontextv1alpha1.RuntimeSpec{Image: "test/runtime"},
				},
			}
		},
		"AgentRun": func(name string, budget *kontextv1alpha1.BudgetSpec) client.Object {
			return &kontextv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: kontextv1alpha1.AgentRunSpec{
					Goal:    "test budget validation",
					Model:   "test/model",
					Budget:  budget,
					Runtime: kontextv1alpha1.RuntimeSpec{Image: "test/runtime"},
				},
			}
		},
	}

	for kind, objectFactory := range objectFactories {
		t.Run(kind, func(t *testing.T) {
			for testIndex, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					object := objectFactory(fmt.Sprintf("budget-validation-%d", testIndex), test.budget)
					err := k8sClient.Create(context.Background(), object)
					if test.wantValid {
						if err != nil {
							t.Fatalf("expected valid object: %v", err)
						}
						if err := k8sClient.Delete(context.Background(), object); err != nil {
							t.Fatalf("delete valid object: %v", err)
						}
						return
					}
					if !apierrors.IsInvalid(err) {
						t.Fatalf("expected API validation error, got %v", err)
					}
				})
			}
		})
	}
}

func int32Ptr(value int32) *int32 {
	return &value
}

func float64Ptr(value float64) *float64 {
	return &value
}
