package podbuilder

import (
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kontextv1alpha1 "github.com/kontext-dev/kontext/api/v1alpha1"
	"github.com/kontext-dev/kontext/internal/runtimepolicy"
	"github.com/kontext-dev/kontext/internal/util"
)

const (
	LabelRunName   = "kontext.dev/run"
	LabelAgentName = "kontext.dev/agent"
)

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// BuildPod constructs a Pod for the given AgentRun.
func BuildPod(run *kontextv1alpha1.AgentRun) *corev1.Pod {
	podName := PodNameForRun(run.Name)
	labels := map[string]string{
		"app.kubernetes.io/name":      "kontext-agentrun",
		LabelRunName:                  run.Name,
		"app.kubernetes.io/component": "runtime",
	}
	if run.Spec.AgentRef != nil && run.Spec.AgentRef.Name != "" {
		labels[LabelAgentName] = run.Spec.AgentRef.Name
	}

	env := buildEnv(run)
	volumes, volumeMounts := buildKnowledgeVolumes(run)

	container := corev1.Container{
		Name:            "runtime",
		Image:           run.Spec.Runtime.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             env,
		VolumeMounts:    volumeMounts,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resourceQuantity("50m"),
				corev1.ResourceMemory: resourceQuantity("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resourceQuantity("500m"),
				corev1.ResourceMemory: resourceQuantity("256Mi"),
			},
		},
	}
	if len(run.Spec.Runtime.Command) > 0 {
		container.Command = util.CloneSlice(run.Spec.Runtime.Command)
	}
	if len(run.Spec.Runtime.Args) > 0 {
		container.Args = util.CloneSlice(run.Spec.Runtime.Args)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: run.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{container},
			Volumes:       volumes,
		},
	}

	if run.Spec.ServiceAccountName != "" {
		pod.Spec.ServiceAccountName = run.Spec.ServiceAccountName
	}

	return pod
}

func buildEnv(run *kontextv1alpha1.AgentRun) []corev1.EnvVar {
	provider := runtimepolicy.NormalizeProvider(run.Spec.Provider)
	tools := strings.Join(run.Spec.Tools, ",")
	budget := run.Spec.Budget

	env := []corev1.EnvVar{
		{Name: "KONTEXT_RUN_NAME", Value: run.Name},
		{Name: "KONTEXT_AGENT_NAME", Value: agentName(run)},
		{Name: "KONTEXT_GOAL", Value: run.Spec.Goal},
		{Name: "KONTEXT_PROVIDER", Value: provider},
		{Name: "KONTEXT_MODEL", Value: run.Spec.Model},
		{Name: "KONTEXT_TOOLS", Value: tools},
		{Name: "KONTEXT_BUDGET_TOKENS", Value: budgetField(budget, "tokens")},
		{Name: "KONTEXT_BUDGET_WALLCLOCK", Value: budgetField(budget, "wallclock")},
		{Name: "KONTEXT_BUDGET_DOLLARS", Value: budgetField(budget, "dollars")},
	}

	for _, extra := range run.Spec.Env {
		env = append(env, corev1.EnvVar{Name: extra.Name, Value: extra.Value})
	}

	if runtimepolicy.NeedsAPIKey(provider) {
		secretName := runtimepolicy.SecretName(provider, run.Spec.SecretRef)
		for _, credential := range runtimepolicy.Credentials(provider) {
			env = append(env, corev1.EnvVar{
				Name: credential.EnvVarName,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
						Key:                  credential.SecretKey,
					},
				},
			})
		}
	}

	return env
}

func buildKnowledgeVolumes(run *kontextv1alpha1.AgentRun) ([]corev1.Volume, []corev1.VolumeMount) {
	if run.Spec.KnowledgeConfigMapRef == nil || run.Spec.KnowledgeConfigMapRef.Name == "" {
		return nil, nil
	}

	configMapName := run.Spec.KnowledgeConfigMapRef.Name
	volumeName := "kontext-knowledge"
	return []corev1.Volume{
			{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
					},
				},
			},
		}, []corev1.VolumeMount{
			{
				Name:      volumeName,
				MountPath: "/kontext/knowledge",
				ReadOnly:  true,
			},
		}
}

func agentName(run *kontextv1alpha1.AgentRun) string {
	if run.Spec.AgentRef != nil && run.Spec.AgentRef.Name != "" {
		return run.Spec.AgentRef.Name
	}
	return run.Name
}

func budgetField(budget *kontextv1alpha1.BudgetSpec, field string) string {
	if budget == nil {
		return ""
	}
	switch field {
	case "tokens":
		if budget.Tokens != nil {
			return fmt.Sprintf("%d", *budget.Tokens)
		}
	case "wallclock":
		return budget.Wallclock
	case "dollars":
		if budget.Dollars != nil {
			return fmt.Sprintf("%g", *budget.Dollars)
		}
	default:
		return ""
	}
	return ""
}

// PodNameForRun returns a deterministic Pod name for an AgentRun.
func PodNameForRun(runName string) string {
	safe := invalidNameChars.ReplaceAllString(strings.ToLower(runName), "-")
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "run"
	}
	if len(safe) > 56 {
		safe = safe[:56]
	}
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "run"
	}
	return fmt.Sprintf("run-%s", safe)
}

func resourceQuantity(value string) resource.Quantity {
	return resource.MustParse(value)
}
