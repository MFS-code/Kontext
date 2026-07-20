// Package runfactory builds immutable AgentRun snapshots from Agent definitions.
package runfactory

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/runtimepolicy"
)

// NewForAgent builds an owned AgentRun with a fully resolved snapshot of the
// Agent execution fields and the supplied concrete goal.
func NewForAgent(
	agent *kontextv1alpha1.Agent,
	runName string,
	goal string,
	scheme *runtime.Scheme,
) (*kontextv1alpha1.AgentRun, error) {
	agentSpec := agent.Spec.DeepCopy()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: agent.Namespace,
			Labels: map[string]string{
				podbuilder.LabelAgentName: agent.Name,
			},
		},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef:              &kontextv1alpha1.AgentRef{Name: agent.Name},
			Goal:                  goal,
			Provider:              runtimepolicy.NormalizeProvider(agentSpec.Provider),
			Model:                 agentSpec.Model,
			Tools:                 agentSpec.Tools,
			Budget:                agentSpec.Budget,
			SecretRef:             agentSpec.SecretRef,
			KnowledgeConfigMapRef: agentSpec.KnowledgeConfigMapRef,
			ServiceAccountName:    agentSpec.ServiceAccountName,
			Runtime:               agentSpec.Runtime,
			Env:                   agentSpec.Env,
		},
	}
	if err := controllerutil.SetControllerReference(agent, run, scheme); err != nil {
		return nil, err
	}
	return run, nil
}
