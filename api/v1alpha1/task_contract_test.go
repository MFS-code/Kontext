package v1alpha1_test

import (
	"encoding/json"
	"strings"
	"testing"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestSparseTaskInvocationJSONOmitsExecutionFields(t *testing.T) {
	invocation := kontextv1alpha1.AgentRunSpec{
		AgentRef: &kontextv1alpha1.AgentRef{Name: "task"},
		Parameters: map[string]string{
			"input": "value",
		},
	}

	data, err := json.Marshal(invocation)
	if err != nil {
		t.Fatalf("marshal sparse invocation: %v", err)
	}
	encoded := string(data)
	for _, field := range []string{
		`"goal"`,
		`"provider"`,
		`"model"`,
		`"tools"`,
		`"budget"`,
		`"secretRef"`,
		`"knowledgeConfigMapRef"`,
		`"serviceAccountName"`,
		`"runtime"`,
		`"env"`,
	} {
		if strings.Contains(encoded, field) {
			t.Fatalf("sparse invocation unexpectedly serialized %s: %s", field, encoded)
		}
	}
	if !strings.Contains(encoded, `"agentRef":{"name":"task"}`) ||
		!strings.Contains(encoded, `"parameters":{"input":"value"}`) {
		t.Fatalf("sparse invocation omitted allowed fields: %s", encoded)
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
