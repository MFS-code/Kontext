package v1alpha1_test

import (
	"encoding/json"
	"testing"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestSparseReferencedCreateRequestDecodesForAdmissionMutation(t *testing.T) {
	// Kubernetes invokes mutating admission before it validates the final
	// object against the CRD. The invocation webhook decodes this request shape,
	// resolves it in memory, and returns a complete object. It must never
	// marshal or persist this unresolved value.
	data := []byte(`{"agentRef":{"name":"task"},"parameters":{"input":"value"}}`)
	var invocation kontextv1alpha1.AgentRunSpec
	if err := json.Unmarshal(data, &invocation); err != nil {
		t.Fatalf("decode sparse CREATE request: %v", err)
	}
	if invocation.AgentRef == nil || invocation.AgentRef.Name != "task" {
		t.Fatalf("decoded agentRef = %#v", invocation.AgentRef)
	}
	if invocation.Parameters["input"] != "value" {
		t.Fatalf("decoded parameters = %#v", invocation.Parameters)
	}
	if invocation.Goal != "" || invocation.Model != "" || invocation.Runtime.Image != "" {
		t.Fatalf("sparse request unexpectedly contains execution fields: %#v", invocation)
	}
}

func TestAgentRunParametersDeepCopy(t *testing.T) {
	source := &kontextv1alpha1.AgentRun{
		Spec: kontextv1alpha1.AgentRunSpec{
			Parameters: map[string]string{"input": "original"},
		},
	}
	copy := source.DeepCopy()
	source.Spec.Parameters["input"] = "changed"

	if copy.Spec.Parameters["input"] != "original" {
		t.Fatalf("deep copy shares parameters map: %#v", copy.Spec.Parameters)
	}
}

func TestDeliverySpecsDeepCopy(t *testing.T) {
	agent := &kontextv1alpha1.Agent{
		Spec: kontextv1alpha1.AgentSpec{
			Runtime: kontextv1alpha1.RuntimeSpec{
				Delivery: &kontextv1alpha1.RuntimeDeliverySpec{Port: 8080},
			},
		},
	}
	run := &kontextv1alpha1.AgentRun{
		Spec: kontextv1alpha1.AgentRunSpec{
			Delivery: &kontextv1alpha1.AgentRunDeliverySpec{Port: 8080},
		},
	}

	agentCopy := agent.DeepCopy()
	runCopy := run.DeepCopy()
	agent.Spec.Runtime.Delivery.Port = 9090
	run.Spec.Delivery.Port = 9090

	if agentCopy.Spec.Runtime.Delivery.Port != 8080 {
		t.Fatalf("Agent deep copy shares runtime delivery: %#v", agentCopy.Spec.Runtime.Delivery)
	}
	if runCopy.Spec.Delivery.Port != 8080 {
		t.Fatalf("AgentRun deep copy shares delivery snapshot: %#v", runCopy.Spec.Delivery)
	}
}
