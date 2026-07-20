package runfactory_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/runfactory"
)

func TestResolveTaskInterpolation(t *testing.T) {
	tests := []struct {
		name       string
		template   string
		parameters map[string]string
		want       string
		wantCode   runfactory.ResolutionErrorCode
	}{
		{name: "placeholder at start", template: "${name} suffix", parameters: map[string]string{"name": "prefix"}, want: "prefix suffix"},
		{name: "placeholder in middle", template: "before ${name} after", parameters: map[string]string{"name": "middle"}, want: "before middle after"},
		{name: "placeholder at end", template: "prefix ${name}", parameters: map[string]string{"name": "suffix"}, want: "prefix suffix"},
		{name: "adjacent placeholders", template: "${first}${second}", parameters: map[string]string{"first": "a", "second": "b"}, want: "ab"},
		{name: "repeated parameter", template: "${name}/${name}", parameters: map[string]string{"name": "same"}, want: "same/same"},
		{name: "empty value", template: "before${value}after", parameters: map[string]string{"value": ""}, want: "beforeafter"},
		{name: "Unicode value", template: "say ${value}", parameters: map[string]string{"value": "こんにちは 🌍"}, want: "say こんにちは 🌍"},
		{name: "multiline value", template: "begin\n${value}\nend", parameters: map[string]string{"value": "one\ntwo"}, want: "begin\none\ntwo\nend"},
		{name: "escaped placeholder", template: "$${name}", want: "${name}"},
		{name: "escaped and resolved", template: "$${literal} ${value}", parameters: map[string]string{"value": "resolved"}, want: "${literal} resolved"},
		{name: "replacement is not re-expanded", template: "${value}", parameters: map[string]string{"value": "${other}"}, want: "${other}"},
		{name: "ordinary dollar is literal", template: "cost $5", want: "cost $5"},
		{name: "missing parameter", template: "${missing}", wantCode: runfactory.ErrorMissingParameters},
		{name: "missing names are stable", template: "${z} ${a}", wantCode: runfactory.ErrorMissingParameters},
		{name: "unused parameter", template: "constant", parameters: map[string]string{"unused": "value"}, wantCode: runfactory.ErrorUnusedParameters},
		{name: "escaped parameter is unused", template: "$${name}", parameters: map[string]string{"name": "value"}, wantCode: runfactory.ErrorUnusedParameters},
		{name: "empty placeholder", template: "${}", wantCode: runfactory.ErrorInvalidTemplate},
		{name: "unterminated placeholder", template: "${name", wantCode: runfactory.ErrorInvalidTemplate},
		{name: "invalid leading digit", template: "${1name}", wantCode: runfactory.ErrorInvalidTemplate},
		{name: "invalid punctuation", template: "${bad-name}", wantCode: runfactory.ErrorInvalidTemplate},
		{name: "nested placeholder", template: "${outer${inner}}", wantCode: runfactory.ErrorInvalidTemplate},
		{name: "malformed escaped placeholder", template: "$${name", wantCode: runfactory.ErrorInvalidTemplate},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := taskAgent("task", "", test.template)
			invocation := taskInvocation("task-run", "task", test.parameters)

			got, err := runfactory.ResolveTask(agent, invocation, taskResolverScheme(t))
			if test.wantCode != "" {
				assertResolutionErrorCode(t, err, test.wantCode)
				if got != nil {
					t.Fatalf("expected no resolved run, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve Task: %v", err)
			}
			if got.Spec.Goal != test.want {
				t.Fatalf("resolved goal = %q, want %q", got.Spec.Goal, test.want)
			}
		})
	}
}

func TestResolveTaskStaticGoal(t *testing.T) {
	tests := []struct {
		name       string
		goal       string
		template   string
		parameters map[string]string
		wantCode   runfactory.ResolutionErrorCode
	}{
		{name: "static goal", goal: "run the task"},
		{name: "static goal rejects parameters", goal: "run the task", parameters: map[string]string{"input": "value"}, wantCode: runfactory.ErrorUnusedParameters},
		{name: "static goal rejects allocated empty parameters", goal: "run the task", parameters: map[string]string{}, wantCode: runfactory.ErrorUnusedParameters},
		{name: "neither goal nor template", wantCode: runfactory.ErrorInvalidTemplate},
		{name: "both goal and template", goal: "static", template: "${input}", wantCode: runfactory.ErrorInvalidTemplate},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := taskAgent("task", test.goal, test.template)
			invocation := taskInvocation("task-run", "task", test.parameters)
			got, err := runfactory.ResolveTask(agent, invocation, taskResolverScheme(t))
			if test.wantCode != "" {
				assertResolutionErrorCode(t, err, test.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("resolve static Task: %v", err)
			}
			if got.Spec.Goal != test.goal {
				t.Fatalf("resolved goal = %q, want %q", got.Spec.Goal, test.goal)
			}
		})
	}
}

func TestResolveTaskRejectsEveryLockedExecutionField(t *testing.T) {
	emptyString := ""
	tests := []struct {
		name  string
		field string
		apply func(*kontextv1alpha1.AgentRunSpec)
	}{
		{name: "goal", field: "goal", apply: func(spec *kontextv1alpha1.AgentRunSpec) { spec.Goal = "override" }},
		{name: "provider", field: "provider", apply: func(spec *kontextv1alpha1.AgentRunSpec) { spec.Provider = "fake" }},
		{name: "model", field: "model", apply: func(spec *kontextv1alpha1.AgentRunSpec) { spec.Model = "override" }},
		{name: "tools", field: "tools", apply: func(spec *kontextv1alpha1.AgentRunSpec) { spec.Tools = []string{} }},
		{name: "budget", field: "budget", apply: func(spec *kontextv1alpha1.AgentRunSpec) { spec.Budget = &kontextv1alpha1.BudgetSpec{} }},
		{name: "secretRef", field: "secretRef", apply: func(spec *kontextv1alpha1.AgentRunSpec) { spec.SecretRef = &kontextv1alpha1.SecretRef{} }},
		{name: "knowledgeConfigMapRef", field: "knowledgeConfigMapRef", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.KnowledgeConfigMapRef = &kontextv1alpha1.ConfigMapRef{}
		}},
		{name: "serviceAccountName", field: "serviceAccountName", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.ServiceAccountName = "runner"
		}},
		{name: "runtime image", field: "runtime", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.Runtime.Image = "override"
		}},
		{name: "runtime empty command", field: "runtime", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.Runtime.Command = []string{}
		}},
		{name: "runtime empty args", field: "runtime", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.Runtime.Args = []string{}
		}},
		{name: "runtime empty result", field: "runtime", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.Runtime.Result = &kontextv1alpha1.RuntimeResultSpec{}
		}},
		{name: "runtime empty security context", field: "runtime", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.Runtime.SecurityContext = &kontextv1alpha1.RuntimeSecurityContext{}
		}},
		{name: "runtime nested capabilities", field: "runtime", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.Runtime.SecurityContext = &kontextv1alpha1.RuntimeSecurityContext{
				Capabilities: &kontextv1alpha1.RuntimeCapabilities{Drop: []string{}},
			}
		}},
		{name: "runtime nested seccomp", field: "runtime", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.Runtime.SecurityContext = &kontextv1alpha1.RuntimeSecurityContext{
				SeccompProfile: &kontextv1alpha1.RuntimeSeccompProfile{},
			}
		}},
		{name: "env", field: "env", apply: func(spec *kontextv1alpha1.AgentRunSpec) { spec.Env = []kontextv1alpha1.EnvVar{} }},
		{name: "env nested literal", field: "env", apply: func(spec *kontextv1alpha1.AgentRunSpec) {
			spec.Env = []kontextv1alpha1.EnvVar{{Name: "EMPTY", Value: &emptyString}}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := taskAgent("task", "source goal", "")
			invocation := taskInvocation("task-run", "task", nil)
			test.apply(&invocation.Spec)

			got, err := runfactory.ResolveTask(agent, invocation, taskResolverScheme(t))
			resolutionErr := assertResolutionErrorCode(t, err, runfactory.ErrorConflictingFields)
			if got != nil {
				t.Fatalf("expected no resolved run, got %#v", got)
			}
			if !reflect.DeepEqual(resolutionErr.Names, []string{test.field}) {
				t.Fatalf("conflicting fields = %v, want [%s]", resolutionErr.Names, test.field)
			}
		})
	}
}

func TestResolveTaskReportsStableErrors(t *testing.T) {
	scheme := taskResolverScheme(t)
	tests := []struct {
		name       string
		agent      *kontextv1alpha1.Agent
		invocation *kontextv1alpha1.AgentRun
		wantCode   runfactory.ResolutionErrorCode
		wantText   string
	}{
		{
			name:       "nil Agent",
			invocation: taskInvocation("run", "missing", nil),
			wantCode:   runfactory.ErrorMissingAgent,
			wantText:   `Task resolution failed [MissingAgent]: Agent "missing" was not found`,
		},
		{
			name:       "missing reference",
			agent:      taskAgent("task", "goal", ""),
			invocation: taskInvocation("run", "", nil),
			wantCode:   runfactory.ErrorMissingAgent,
			wantText:   `Task resolution failed [MissingAgent]: Agent "" was not found`,
		},
		{
			name:       "reference mismatch",
			agent:      taskAgent("task", "goal", ""),
			invocation: taskInvocation("run", "other", nil),
			wantCode:   runfactory.ErrorMissingAgent,
			wantText:   `Task resolution failed [MissingAgent]: Agent "other" was not found`,
		},
		{
			name: "wrong mode",
			agent: &kontextv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "service", Namespace: "default"},
				Spec:       kontextv1alpha1.AgentSpec{Mode: kontextv1alpha1.AgentModeService},
			},
			invocation: taskInvocation("run", "service", nil),
			wantCode:   runfactory.ErrorWrongMode,
			wantText:   `Task resolution failed [WrongMode]: Agent "service" has mode "Service"`,
		},
		{
			name:       "sorted missing parameters",
			agent:      taskAgent("task", "${z} ${a}", ""),
			invocation: taskInvocation("run", "task", nil),
			wantCode:   runfactory.ErrorInvalidTemplate,
			wantText:   `Task resolution failed [InvalidTemplate]: Task Agent must configure exactly one of goal or goalTemplate`,
		},
		{
			name:       "sorted conflicting fields",
			agent:      taskAgent("task", "goal", ""),
			invocation: taskInvocation("run", "task", nil),
			wantCode:   runfactory.ErrorConflictingFields,
			wantText:   `Task resolution failed [ConflictingFields]: invocation supplies locked fields: env, tools`,
		},
	}
	tests[4].agent = taskAgent("task", "", "${z} ${a}")
	tests[4].wantCode = runfactory.ErrorMissingParameters
	tests[4].wantText = `Task resolution failed [MissingParameters]: missing parameters: a, z`
	tests[5].invocation.Spec.Tools = []string{}
	tests[5].invocation.Spec.Env = []kontextv1alpha1.EnvVar{}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runfactory.ResolveTask(test.agent, test.invocation, scheme)
			assertResolutionErrorCode(t, err, test.wantCode)
			if err.Error() != test.wantText {
				t.Fatalf("error = %q, want %q", err.Error(), test.wantText)
			}
		})
	}
}

func TestResolveTaskBuildsIsolatedSnapshot(t *testing.T) {
	agent := taskAgent("task", "", "process ${input}")
	agent.UID = types.UID("task-uid")
	agent.Spec.Provider = " OpenAI-Compatible "
	agent.Spec.Tools = []string{"shell"}
	agent.Spec.Budget = &kontextv1alpha1.BudgetSpec{Tokens: pointer(int32(42))}
	agent.Spec.SecretRef = &kontextv1alpha1.SecretRef{Name: "provider"}
	agent.Spec.KnowledgeConfigMapRef = &kontextv1alpha1.ConfigMapRef{Name: "knowledge"}
	agent.Spec.ServiceAccountName = "runner"
	agent.Spec.Runtime = kontextv1alpha1.RuntimeSpec{
		Image:   "example/runtime:v1",
		Command: []string{"/runtime"},
		Args:    []string{"task"},
		SecurityContext: &kontextv1alpha1.RuntimeSecurityContext{
			Capabilities: &kontextv1alpha1.RuntimeCapabilities{Drop: []string{"ALL"}},
		},
	}
	value := "literal"
	agent.Spec.Env = []kontextv1alpha1.EnvVar{{Name: "VALUE", Value: &value}}
	invocation := taskInvocation("user-run", "task", map[string]string{"input": "résumé\ntext"})
	invocation.TypeMeta = metav1.TypeMeta{APIVersion: "kontext.dev/v1alpha1", Kind: "AgentRun"}
	invocation.Labels = map[string]string{"user": "label", podbuilder.LabelAgentName: "wrong"}
	invocation.Annotations = map[string]string{"note": "keep"}
	invocation.Finalizers = []string{"example/finalizer"}
	agentBefore := agent.DeepCopy()
	invocationBefore := invocation.DeepCopy()

	got, err := runfactory.ResolveTask(agent, invocation, taskResolverScheme(t))
	if err != nil {
		t.Fatalf("resolve Task: %v", err)
	}
	if !reflect.DeepEqual(agent, agentBefore) {
		t.Fatal("resolver mutated Agent input")
	}
	if !reflect.DeepEqual(invocation, invocationBefore) {
		t.Fatal("resolver mutated invocation input")
	}
	if got.Spec.Goal != "process résumé\ntext" {
		t.Fatalf("resolved goal = %q", got.Spec.Goal)
	}
	if got.Spec.Provider != "openai-compatible" {
		t.Fatalf("provider = %q, want normalized openai-compatible", got.Spec.Provider)
	}
	if got.Labels["user"] != "label" || got.Labels[podbuilder.LabelAgentName] != "task" {
		t.Fatalf("labels were not preserved and normalized: %#v", got.Labels)
	}
	if got.Annotations["note"] != "keep" || !reflect.DeepEqual(got.Finalizers, []string{"example/finalizer"}) {
		t.Fatalf("invocation metadata was not preserved: %#v", got.ObjectMeta)
	}
	if !metav1.IsControlledBy(got, agent) {
		t.Fatalf("resolved run is not controlled by Agent: %#v", got.OwnerReferences)
	}

	agent.Spec.Tools[0] = "changed"
	*agent.Spec.Budget.Tokens = 99
	agent.Spec.Runtime.Command[0] = "/changed"
	agent.Spec.Runtime.SecurityContext.Capabilities.Drop[0] = "CHANGED"
	*agent.Spec.Env[0].Value = "changed"
	invocation.Spec.Parameters["input"] = "changed"
	invocation.Labels["user"] = "changed"
	invocation.Annotations["note"] = "changed"
	invocation.Finalizers[0] = "changed"

	if got.Spec.Tools[0] != "shell" ||
		*got.Spec.Budget.Tokens != 42 ||
		got.Spec.Runtime.Command[0] != "/runtime" ||
		got.Spec.Runtime.SecurityContext.Capabilities.Drop[0] != "ALL" ||
		*got.Spec.Env[0].Value != "literal" ||
		got.Spec.Parameters["input"] != "résumé\ntext" ||
		got.Labels["user"] != "label" ||
		got.Annotations["note"] != "keep" ||
		got.Finalizers[0] != "example/finalizer" {
		t.Fatalf("resolved run shares data with an input: %#v", got)
	}
}

func FuzzResolveTask(f *testing.F) {
	seeds := []struct {
		template string
		key      string
		value    string
	}{
		{template: "${input}", key: "input", value: "value"},
		{template: "$${input}", key: "", value: ""},
		{template: "${input}/${input}", key: "input", value: ""},
		{template: "before ${input} after", key: "input", value: "こんにちは\nworld"},
		{template: "${missing}", key: "unused", value: "value"},
		{template: "${", key: "", value: ""},
		{template: "${bad-name}", key: "bad-name", value: "value"},
	}
	for _, seed := range seeds {
		f.Add(seed.template, seed.key, seed.value)
	}

	scheme := taskResolverScheme(f)
	f.Fuzz(func(t *testing.T, template, key, value string) {
		agent := taskAgent("task", "", template)
		var parameters map[string]string
		if key != "" {
			parameters = map[string]string{key: value}
		}
		invocation := taskInvocation("run", "task", parameters)
		agentBefore := agent.DeepCopy()
		invocationBefore := invocation.DeepCopy()

		first, firstErr := runfactory.ResolveTask(agent, invocation, scheme)
		second, secondErr := runfactory.ResolveTask(agent, invocation, scheme)

		if !reflect.DeepEqual(agent, agentBefore) {
			t.Fatal("resolver mutated Agent input")
		}
		if !reflect.DeepEqual(invocation, invocationBefore) {
			t.Fatal("resolver mutated invocation input")
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("non-deterministic result:\nfirst:  %#v\nsecond: %#v", first, second)
		}
		if errorText(firstErr) != errorText(secondErr) {
			t.Fatalf("non-deterministic errors: %q and %q", errorText(firstErr), errorText(secondErr))
		}
		for _, err := range []error{firstErr, secondErr} {
			if err == nil {
				continue
			}
			var resolutionErr *runfactory.ResolutionError
			if !errors.As(err, &resolutionErr) {
				t.Fatalf("resolver returned untyped error %T: %v", err, err)
			}
		}
	})
}

func taskAgent(name, goal, goalTemplate string) *kontextv1alpha1.Agent {
	return &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:         kontextv1alpha1.AgentModeTask,
			Goal:         goal,
			GoalTemplate: goalTemplate,
			Provider:     "fake",
			Model:        "test/model",
			Runtime:      kontextv1alpha1.RuntimeSpec{Image: "example/runtime:test"},
		},
	}
}

func taskInvocation(name, agentName string, parameters map[string]string) *kontextv1alpha1.AgentRun {
	var agentRef *kontextv1alpha1.AgentRef
	if agentName != "" {
		agentRef = &kontextv1alpha1.AgentRef{Name: agentName}
	}
	return &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef:   agentRef,
			Parameters: parameters,
		},
	}
}

func taskResolverScheme(tb testing.TB) *runtime.Scheme {
	tb.Helper()
	scheme := runtime.NewScheme()
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		tb.Fatalf("add Kontext types to scheme: %v", err)
	}
	return scheme
}

func assertResolutionErrorCode(
	t *testing.T,
	err error,
	want runfactory.ResolutionErrorCode,
) *runfactory.ResolutionError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected resolution error %s", want)
	}
	var resolutionErr *runfactory.ResolutionError
	if !errors.As(err, &resolutionErr) {
		t.Fatalf("error type = %T, want *runfactory.ResolutionError: %v", err, err)
	}
	if resolutionErr.Code != want {
		t.Fatalf("error code = %s, want %s: %v", resolutionErr.Code, want, err)
	}
	if !sortStringsEqual(resolutionErr.Names) {
		t.Fatalf("error names are not sorted: %v", resolutionErr.Names)
	}
	return resolutionErr
}

func sortStringsEqual(values []string) bool {
	for index := 1; index < len(values); index++ {
		if strings.Compare(values[index-1], values[index]) > 0 {
			return false
		}
	}
	return true
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func pointer[T any](value T) *T {
	return &value
}
