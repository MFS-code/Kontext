package podbuilder_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/podbuilder"
)

func TestBuildPodInjectsKontextEnv(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "review-task", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "review files",
			Model:    "claude-sonnet-4-6",
			Provider: "echo",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image: "kontext-echo:dev",
			},
			Env: []kontextv1alpha1.EnvVar{
				{Name: "KONTEXT_ZONE_ID", Value: "agent:leaf:0001"},
			},
		},
	}

	pod := podbuilder.BuildPod(run)
	if pod.Name != "run-review-task" {
		t.Fatalf("unexpected pod name: %s", pod.Name)
	}
	if pod.Spec.RestartPolicy != "Never" {
		t.Fatalf("expected Never restart policy")
	}

	env := envMap(pod)
	for _, key := range []string{"KONTEXT_RUN_NAME", "KONTEXT_GOAL", "KONTEXT_MODEL", "KONTEXT_ZONE_ID"} {
		if env[key] == "" {
			t.Fatalf("expected env %s to be set", key)
		}
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("echo provider should not inject API key env")
	}
}

func TestBuildPodMountsKnowledgeConfigMap(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "knowledge-task", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "review files",
			Model:    "echo-model",
			Provider: "echo",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image: "kontext-echo:dev",
			},
			KnowledgeConfigMapRef: &kontextv1alpha1.ConfigMapRef{Name: "runtime-knowledge"},
		},
	}

	pod := podbuilder.BuildPod(run)
	if len(pod.Spec.Volumes) != 1 {
		t.Fatalf("expected one knowledge volume, got %d", len(pod.Spec.Volumes))
	}
	if pod.Spec.Volumes[0].ConfigMap.Name != "runtime-knowledge" {
		t.Fatalf("unexpected configmap name: %s", pod.Spec.Volumes[0].ConfigMap.Name)
	}
}

func TestBuildPodLeavesRuntimePermissionsUntouched(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "permission-task", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "do work",
			Model:    "model",
			Provider: "echo",
			Runtime:  kontextv1alpha1.RuntimeSpec{Image: "runtime:dev"},
		},
	}

	pod := podbuilder.BuildPod(run)
	if pod.Spec.SecurityContext != nil {
		t.Fatalf("expected no pod security context, got %+v", pod.Spec.SecurityContext)
	}
	if pod.Spec.AutomountServiceAccountToken != nil {
		t.Fatalf("expected default token automount behavior, got %v", *pod.Spec.AutomountServiceAccountToken)
	}
	if pod.Spec.ServiceAccountName != "" {
		t.Fatalf("expected empty service account name, got %q", pod.Spec.ServiceAccountName)
	}
	sc := pod.Spec.Containers[0].SecurityContext
	if sc != nil {
		t.Fatalf("expected no container security context, got %+v", sc)
	}
}

func TestBuildPodSetsRequestedServiceAccount(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "sa-task", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:               "do work",
			Model:              "model",
			Provider:           "echo",
			ServiceAccountName: "agent-sa",
			Runtime:            kontextv1alpha1.RuntimeSpec{Image: "runtime:dev"},
		},
	}

	pod := podbuilder.BuildPod(run)
	if pod.Spec.ServiceAccountName != "agent-sa" {
		t.Fatalf("expected service account agent-sa, got %s", pod.Spec.ServiceAccountName)
	}
	if pod.Spec.AutomountServiceAccountToken != nil {
		t.Fatal("expected automount left to Kubernetes defaults when a service account is requested")
	}
	if pod.Spec.SecurityContext != nil {
		t.Fatalf("expected no pod security context, got %+v", pod.Spec.SecurityContext)
	}
	if pod.Spec.Containers[0].SecurityContext != nil {
		t.Fatalf("expected no container security context, got %+v", pod.Spec.Containers[0].SecurityContext)
	}
}

func TestBuildPodInjectsProviderCredentials(t *testing.T) {
	cases := map[string]struct {
		provider   string
		envName    string
		secretName string
		secretKey  string
	}{
		"anthropic":    {provider: "anthropic", envName: "ANTHROPIC_API_KEY", secretName: "kontext-anthropic", secretKey: "ANTHROPIC_API_KEY"},
		"openai":       {provider: "openai", envName: "OPENAI_API_KEY", secretName: "kontext-openai", secretKey: "OPENAI_API_KEY"},
		"google":       {provider: "google", envName: "GOOGLE_API_KEY", secretName: "kontext-google", secretKey: "GOOGLE_API_KEY"},
		"azure-openai": {provider: "azure-openai", envName: "AZURE_OPENAI_API_KEY", secretName: "kontext-azure-openai", secretKey: "AZURE_OPENAI_API_KEY"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run := &kontextv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{Name: "cred-task", Namespace: "default"},
				Spec: kontextv1alpha1.AgentRunSpec{
					Goal:     "do work",
					Model:    "model",
					Provider: tc.provider,
					Runtime:  kontextv1alpha1.RuntimeSpec{Image: "runtime:dev"},
				},
			}
			pod := podbuilder.BuildPod(run)
			var found *corev1.EnvVar
			for i := range pod.Spec.Containers[0].Env {
				if pod.Spec.Containers[0].Env[i].Name == tc.envName {
					found = &pod.Spec.Containers[0].Env[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("expected env %s", tc.envName)
			}
			if found.ValueFrom == nil || found.ValueFrom.SecretKeyRef == nil {
				t.Fatalf("expected secret ref for %s", tc.envName)
			}
			if found.ValueFrom.SecretKeyRef.Name != tc.secretName {
				t.Fatalf("expected secret %s, got %s", tc.secretName, found.ValueFrom.SecretKeyRef.Name)
			}
			if found.ValueFrom.SecretKeyRef.Key != tc.secretKey {
				t.Fatalf("expected secret key %s, got %s", tc.secretKey, found.ValueFrom.SecretKeyRef.Key)
			}
		})
	}
}

func TestBuildPodInjectsAllBedrockCredentials(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "bedrock-task", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "do work",
			Model:    "model",
			Provider: "bedrock",
			Runtime:  kontextv1alpha1.RuntimeSpec{Image: "runtime:dev"},
		},
	}
	pod := podbuilder.BuildPod(run)

	expected := map[string]string{
		"AWS_ACCESS_KEY_ID":     "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY": "AWS_SECRET_ACCESS_KEY",
	}
	for _, item := range pod.Spec.Containers[0].Env {
		secretKeyRef := item.ValueFrom
		if expectedKey, ok := expected[item.Name]; ok && secretKeyRef != nil && secretKeyRef.SecretKeyRef != nil {
			if secretKeyRef.SecretKeyRef.Name != "kontext-bedrock" {
				t.Fatalf("expected Bedrock secret, got %s", secretKeyRef.SecretKeyRef.Name)
			}
			if secretKeyRef.SecretKeyRef.Key != expectedKey {
				t.Fatalf("expected key %s, got %s", expectedKey, secretKeyRef.SecretKeyRef.Key)
			}
			delete(expected, item.Name)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing Bedrock credential env vars: %#v", expected)
	}
}

func TestPodNameForRunSanitizes(t *testing.T) {
	if got := podbuilder.PodNameForRun("Review Task!"); got != "run-review-task" {
		t.Fatalf("unexpected sanitized name: %s", got)
	}
}

func TestPodNameForRunTrimsHyphenAfterTruncation(t *testing.T) {
	runName := strings.Repeat("a", 55) + "-" + strings.Repeat("b", 10)
	got := podbuilder.PodNameForRun(runName)
	if strings.HasSuffix(got, "-") {
		t.Fatalf("pod name must not end in a hyphen: %s", got)
	}
	if len(got) > 63 {
		t.Fatalf("pod name exceeds DNS label limit: %d", len(got))
	}
}

func TestBuildPodPopulatesBudgetEnv(t *testing.T) {
	tokens := int32(1000)
	dollars := 2.5
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "budget-task", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "do work",
			Model:    "model",
			Provider: "echo",
			Runtime:  kontextv1alpha1.RuntimeSpec{Image: "runtime:dev"},
			Budget: &kontextv1alpha1.BudgetSpec{
				Tokens:    &tokens,
				Wallclock: "5m",
				Dollars:   &dollars,
			},
		},
	}
	env := envMap(podbuilder.BuildPod(run))
	if env["KONTEXT_BUDGET_TOKENS"] != "1000" {
		t.Fatalf("unexpected tokens budget: %q", env["KONTEXT_BUDGET_TOKENS"])
	}
	if env["KONTEXT_BUDGET_WALLCLOCK"] != "5m" {
		t.Fatalf("unexpected wallclock budget: %q", env["KONTEXT_BUDGET_WALLCLOCK"])
	}
	if env["KONTEXT_BUDGET_DOLLARS"] != "2.5" {
		t.Fatalf("unexpected dollars budget: %q", env["KONTEXT_BUDGET_DOLLARS"])
	}
}

func TestBuildPodEmptyBudgetEnvWhenUnset(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "no-budget", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "do work",
			Model:    "model",
			Provider: "echo",
			Runtime:  kontextv1alpha1.RuntimeSpec{Image: "runtime:dev"},
		},
	}
	env := envMap(podbuilder.BuildPod(run))
	for _, key := range []string{"KONTEXT_BUDGET_TOKENS", "KONTEXT_BUDGET_WALLCLOCK", "KONTEXT_BUDGET_DOLLARS"} {
		if env[key] != "" {
			t.Fatalf("expected %s to be empty, got %q", key, env[key])
		}
	}
}

func TestBuildPodDefaultsAgentNameToRunName(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "standalone-run", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "do work",
			Model:    "model",
			Provider: "echo",
			Runtime:  kontextv1alpha1.RuntimeSpec{Image: "runtime:dev"},
		},
	}
	pod := podbuilder.BuildPod(run)
	if envMap(pod)["KONTEXT_AGENT_NAME"] != "standalone-run" {
		t.Fatalf("expected agent name to default to run name")
	}
	if _, ok := pod.Labels[podbuilder.LabelAgentName]; ok {
		t.Fatalf("expected no agent label for standalone run")
	}
}

func TestBuildPodUsesAgentRefName(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:     "do work",
			Model:    "model",
			Provider: "echo",
			Runtime:  kontextv1alpha1.RuntimeSpec{Image: "runtime:dev"},
			AgentRef: &kontextv1alpha1.AgentRef{Name: "echo-service"},
		},
	}
	pod := podbuilder.BuildPod(run)
	if envMap(pod)["KONTEXT_AGENT_NAME"] != "echo-service" {
		t.Fatalf("expected agent name from AgentRef")
	}
	if pod.Labels[podbuilder.LabelAgentName] != "echo-service" {
		t.Fatalf("expected agent label set from AgentRef, got %q", pod.Labels[podbuilder.LabelAgentName])
	}
}

func TestBuildPodAppliesRuntimeCommandArgsAndServiceAccount(t *testing.T) {
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "cmd-run", Namespace: "default"},
		Spec: kontextv1alpha1.AgentRunSpec{
			Goal:               "do work",
			Model:              "model",
			Provider:           "echo",
			ServiceAccountName: "kontext-agent",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:   "runtime:dev",
				Command: []string{"/bin/agent"},
				Args:    []string{"--verbose", "--goal"},
			},
		},
	}
	pod := podbuilder.BuildPod(run)
	if pod.Spec.ServiceAccountName != "kontext-agent" {
		t.Fatalf("expected service account, got %q", pod.Spec.ServiceAccountName)
	}
	container := pod.Spec.Containers[0]
	if len(container.Command) != 1 || container.Command[0] != "/bin/agent" {
		t.Fatalf("unexpected command: %#v", container.Command)
	}
	if len(container.Args) != 2 || container.Args[0] != "--verbose" {
		t.Fatalf("unexpected args: %#v", container.Args)
	}
}

func TestPodNameForRunEmptyFallsBackToRun(t *testing.T) {
	if got := podbuilder.PodNameForRun("!!!"); got != "run-run" {
		t.Fatalf("expected run-run fallback, got %s", got)
	}
	if got := podbuilder.PodNameForRun(""); got != "run-run" {
		t.Fatalf("expected run-run fallback for empty input, got %s", got)
	}
}

func envMap(pod *corev1.Pod) map[string]string {
	env := map[string]string{}
	for _, item := range pod.Spec.Containers[0].Env {
		if item.Value != "" {
			env[item.Name] = item.Value
		}
	}
	return env
}
