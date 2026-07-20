// Package testsupport provides helpers shared by Go tests.
package testsupport

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
)

// BuildPod constructs a Pod with the default test configuration.
func BuildPod(run *kontextv1alpha1.AgentRun) *corev1.Pod {
	if run.Spec.Runtime.Result != nil {
		panic("BuildPod: runtime.result requires BuildPodWithConfig")
	}
	pod, err := podbuilder.BuildPodWithConfig(run, podbuilder.Config{})
	if err != nil {
		panic(fmt.Sprintf("BuildPod: %v", err))
	}
	return pod
}
