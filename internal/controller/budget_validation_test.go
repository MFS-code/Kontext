package controller_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
)

func TestWallclockBudgetAPIValidation(t *testing.T) {
	tests := []struct {
		name      string
		budget    *kontextv1alpha1.BudgetSpec
		wantValid bool
	}{
		{name: "omitted", wantValid: true},
		{name: "empty", budget: &kontextv1alpha1.BudgetSpec{}, wantValid: true},
		{
			name:      "positive duration",
			budget:    &kontextv1alpha1.BudgetSpec{Wallclock: "1h30m"},
			wantValid: true,
		},
		{
			name:   "invalid duration",
			budget: &kontextv1alpha1.BudgetSpec{Wallclock: "five minutes"},
		},
		{
			name:   "zero duration",
			budget: &kontextv1alpha1.BudgetSpec{Wallclock: "0s"},
		},
		{
			name:   "negative duration",
			budget: &kontextv1alpha1.BudgetSpec{Wallclock: "-1s"},
		},
	}
	objectFactories := []struct {
		name string
		new  func(string, *kontextv1alpha1.BudgetSpec) client.Object
	}{
		{
			name: "Agent",
			new: func(name string, budget *kontextv1alpha1.BudgetSpec) client.Object {
				return &kontextv1alpha1.Agent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: kontextv1alpha1.AgentSpec{
						Mode:    kontextv1alpha1.AgentModeService,
						Goal:    "test wallclock validation",
						Model:   "test/model",
						Budget:  budget,
						Runtime: echoRuntimeSpec(),
					},
				}
			},
		},
		{
			name: "AgentRun",
			new: func(name string, budget *kontextv1alpha1.BudgetSpec) client.Object {
				return &kontextv1alpha1.AgentRun{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: kontextv1alpha1.AgentRunSpec{
						Goal:    "test wallclock validation",
						Model:   "test/model",
						Budget:  budget,
						Runtime: echoRuntimeSpec(),
					},
				}
			},
		},
	}

	for objectIndex, objectFactory := range objectFactories {
		for testIndex, test := range tests {
			t.Run(objectFactory.name+"/"+test.name, func(t *testing.T) {
				name := fmt.Sprintf("wallclock-validation-%d-%d", objectIndex, testIndex)
				object := objectFactory.new(name, test.budget)
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
				if !strings.Contains(err.Error(), "wallclock must be empty or a positive duration") {
					t.Fatalf("validation error is not actionable: %v", err)
				}
			})
		}
	}
}
