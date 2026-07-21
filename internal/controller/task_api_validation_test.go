package controller_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestTaskAgentAPIValidation(t *testing.T) {
	tests := []struct {
		name         string
		mode         kontextv1alpha1.AgentMode
		goal         string
		goalTemplate string
		schedule     string
		backoff      *kontextv1alpha1.BackoffSpec
		wantValid    bool
	}{
		{name: "Task static goal", mode: kontextv1alpha1.AgentModeTask, goal: "static", wantValid: true},
		{name: "Task goal template", mode: kontextv1alpha1.AgentModeTask, goalTemplate: "${input}", wantValid: true},
		{name: "Task neither goal", mode: kontextv1alpha1.AgentModeTask},
		{name: "Task both goals", mode: kontextv1alpha1.AgentModeTask, goal: "static", goalTemplate: "${input}"},
		{name: "Task rejects schedule", mode: kontextv1alpha1.AgentModeTask, goal: "static", schedule: "* * * * *"},
		{name: "Task rejects backoff", mode: kontextv1alpha1.AgentModeTask, goal: "static", backoff: &kontextv1alpha1.BackoffSpec{}},
		{name: "Service static goal", mode: kontextv1alpha1.AgentModeService, goal: "serve", backoff: &kontextv1alpha1.BackoffSpec{}, wantValid: true},
		{name: "Service missing goal", mode: kontextv1alpha1.AgentModeService},
		{name: "Service rejects template", mode: kontextv1alpha1.AgentModeService, goalTemplate: "${input}"},
		{name: "Service rejects schedule", mode: kontextv1alpha1.AgentModeService, goal: "serve", schedule: "* * * * *"},
		{name: "Scheduled static goal", mode: kontextv1alpha1.AgentModeScheduled, goal: "scheduled", schedule: "* * * * *", wantValid: true},
		{name: "Scheduled missing goal", mode: kontextv1alpha1.AgentModeScheduled, schedule: "* * * * *"},
		{name: "Scheduled missing schedule", mode: kontextv1alpha1.AgentModeScheduled, goal: "scheduled"},
		{name: "Scheduled rejects template", mode: kontextv1alpha1.AgentModeScheduled, goalTemplate: "${input}", schedule: "* * * * *"},
		{name: "Scheduled rejects backoff", mode: kontextv1alpha1.AgentModeScheduled, goal: "scheduled", schedule: "* * * * *", backoff: &kontextv1alpha1.BackoffSpec{}},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var schedule *kontextv1alpha1.ScheduleSpec
			if test.schedule != "" {
				schedule = &kontextv1alpha1.ScheduleSpec{Expression: test.schedule}
			}
			agent := &kontextv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("task-agent-validation-%d", index),
					Namespace: "default",
				},
				Spec: kontextv1alpha1.AgentSpec{
					Mode:         test.mode,
					Goal:         test.goal,
					GoalTemplate: test.goalTemplate,
					Model:        "test/model",
					Runtime:      echoRuntimeSpec(),
					Schedule:     schedule,
					Backoff:      test.backoff,
				},
			}
			err := k8sClient.Create(context.Background(), agent)
			if test.wantValid {
				if err != nil {
					t.Fatalf("expected valid Agent: %v", err)
				}
				deleteObject(t, agent)
				return
			}
			if !apierrors.IsInvalid(err) {
				t.Fatalf("expected API validation error, got %v", err)
			}
		})
	}
}

func TestAgentRunPersistenceAPIValidation(t *testing.T) {
	tests := []struct {
		name        string
		spec        map[string]any
		wantValid   bool
		wantMessage string
	}{
		{
			name:        "sparse reference only is not persisted",
			spec:        map[string]any{"agentRef": map[string]any{"name": "task"}},
			wantMessage: "Required value",
		},
		{
			name: "sparse parameters are not persisted",
			spec: map[string]any{
				"agentRef":   map[string]any{"name": "task"},
				"parameters": map[string]any{"input": "value"},
			},
			wantMessage: "Required value",
		},
		{
			name: "parameters require reference",
			spec: mergeSpec(
				completeAgentRunSpec(nil),
				map[string]any{"parameters": map[string]any{"input": "value"}},
			),
			wantMessage: "parameters require agentRef",
		},
		{
			name:      "standalone complete execution",
			spec:      completeAgentRunSpec(nil),
			wantValid: true,
		},
		{
			name:      "fully resolved Service or Scheduled controller run",
			spec:      completeAgentRunSpec(map[string]any{"name": "controller-agent"}),
			wantValid: true,
		},
		{
			name: "fully resolved Task snapshot retains parameters",
			spec: mergeSpec(
				completeAgentRunSpec(map[string]any{"name": "task"}),
				map[string]any{"parameters": map[string]any{"input": "value"}},
			),
			wantValid: true,
		},
		{
			name:        "standalone cannot be sparse",
			spec:        map[string]any{},
			wantMessage: "Required value",
		},
		{
			name: "referenced partial execution is rejected",
			spec: map[string]any{
				"agentRef": map[string]any{"name": "task"},
				"goal":     "partial",
			},
			wantMessage: "Required value",
		},
		{
			name: "runtime image remains required",
			spec: map[string]any{
				"agentRef": map[string]any{"name": "task"},
				"goal":     "resolved",
				"model":    "test/model",
				"runtime":  map[string]any{},
			},
			wantMessage: "image: Required value",
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := unstructuredAgentRun(fmt.Sprintf("task-run-validation-%d", index), test.spec)
			err := k8sClient.Create(context.Background(), run)
			if test.wantValid {
				if err != nil {
					t.Fatalf("expected valid AgentRun: %v", err)
				}
				deleteObject(t, run)
				return
			}
			if !apierrors.IsInvalid(err) {
				t.Fatalf("expected API validation error, got %v", err)
			}
			if test.wantMessage != "" && !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("validation error does not contain %q: %v", test.wantMessage, err)
			}
		})
	}
}

func TestAgentRunParametersAndSpecAreImmutable(t *testing.T) {
	ctx := context.Background()
	run := unstructuredAgentRun(
		"task-run-parameters-immutable",
		mergeSpec(
			completeAgentRunSpec(map[string]any{"name": "task"}),
			map[string]any{"parameters": map[string]any{"input": "original"}},
		),
	)
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create complete resolved AgentRun: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), run)
	})

	if err := unstructured.SetNestedField(run.Object, "changed", "spec", "parameters", "input"); err != nil {
		t.Fatalf("change parameters: %v", err)
	}
	err := k8sClient.Update(ctx, run)
	if !apierrors.IsInvalid(err) {
		t.Fatalf("expected immutable spec validation error, got %v", err)
	}
	if !strings.Contains(err.Error(), "AgentRun spec is immutable") {
		t.Fatalf("immutability error is not actionable: %v", err)
	}
}

func TestAgentRunReconcilerCannotObserveAdmittedSparseSpec(t *testing.T) {
	// This controller-only envtest intentionally installs no webhook. The CRD
	// must reject a sparse CREATE by itself, leaving no unresolved object for
	// the AgentRun controller to observe if admission is absent.
	ctx := context.Background()
	name := "sparse-run-never-admitted"
	run := unstructuredAgentRun(name, map[string]any{
		"agentRef":   map[string]any{"name": "task"},
		"parameters": map[string]any{"input": "value"},
	})
	if err := k8sClient.Create(ctx, run); !apierrors.IsInvalid(err) {
		t.Fatalf("expected sparse CREATE to be rejected, got %v", err)
	}

	key := types.NamespacedName{Name: name, Namespace: "default"}
	reconcileAgentRun(ctx, t, key)

	var observed kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, key, &observed); !apierrors.IsNotFound(err) {
		t.Fatalf("controller could observe sparse AgentRun: object=%#v error=%v", observed, err)
	}
}

func completeAgentRunSpec(agentRef map[string]any) map[string]any {
	spec := map[string]any{
		"goal":  "complete execution",
		"model": "test/model",
		"runtime": map[string]any{
			"image": "kontext-echo:dev",
		},
	}
	if agentRef != nil {
		spec["agentRef"] = agentRef
	}
	return spec
}

func mergeSpec(base, fields map[string]any) map[string]any {
	for key, value := range fields {
		base[key] = value
	}
	return base
}

func unstructuredAgentRun(name string, spec map[string]any) *unstructured.Unstructured {
	run := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kontext.dev/v1alpha1",
			"kind":       "AgentRun",
			"metadata": map[string]any{
				"name":      name,
				"namespace": "default",
			},
			"spec": spec,
		},
	}
	run.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kontext.dev",
		Version: "v1alpha1",
		Kind:    "AgentRun",
	})
	return run
}

func deleteObject(t *testing.T, object client.Object) {
	t.Helper()
	if err := k8sClient.Delete(context.Background(), object); err != nil {
		t.Fatalf("delete valid object: %v", err)
	}
}
