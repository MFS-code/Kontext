package runfactory_test

import (
	"reflect"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/runfactory"
)

func TestNewForAgentBuildsCompleteIndependentSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Kontext types to scheme: %v", err)
	}

	tests := []struct {
		name    string
		agent   *kontextv1alpha1.Agent
		runName string
		goal    string
		want    *kontextv1alpha1.AgentRun
		mutate  func(*kontextv1alpha1.Agent)
	}{
		{
			name: "all execution fields",
			agent: &kontextv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "full-agent",
					Namespace: "agents",
					UID:       types.UID("full-agent-uid"),
				},
				Spec: kontextv1alpha1.AgentSpec{
					Mode:         kontextv1alpha1.AgentModeService,
					Goal:         "source goal",
					GoalTemplate: "ignored {{ .input }}",
					Provider:     " OpenAI ",
					Model:        "gpt-test",
					Tools:        []string{"shell", "read_knowledge"},
					Budget: &kontextv1alpha1.BudgetSpec{
						Tokens:    ptr(int32(1234)),
						Wallclock: "5m",
						Dollars:   ptr(2.5),
					},
					SecretRef:             &kontextv1alpha1.SecretRef{Name: "provider-auth"},
					KnowledgeConfigMapRef: &kontextv1alpha1.ConfigMapRef{Name: "knowledge"},
					ServiceAccountName:    "agent-runner",
					Runtime: kontextv1alpha1.RuntimeSpec{
						Image:   "example/runtime:v1",
						Command: []string{"/runtime"},
						Args:    []string{"serve", "--json"},
						Result: &kontextv1alpha1.RuntimeResultSpec{
							Source: kontextv1alpha1.ResultSourceStdout,
							Format: kontextv1alpha1.ResultFormatKontextEnvelope,
						},
						SecurityContext: &kontextv1alpha1.RuntimeSecurityContext{
							AllowPrivilegeEscalation: ptr(false),
							ReadOnlyRootFilesystem:   ptr(true),
							RunAsNonRoot:             ptr(true),
							Capabilities: &kontextv1alpha1.RuntimeCapabilities{
								Drop: []string{"ALL", "NET_RAW"},
							},
							SeccompProfile: &kontextv1alpha1.RuntimeSeccompProfile{
								Type:             "Localhost",
								LocalhostProfile: "profiles/runtime.json",
							},
						},
					},
					Env: []kontextv1alpha1.EnvVar{
						{Name: "LITERAL", Value: ptr("value")},
						{
							Name: "SECRET",
							ValueFrom: &kontextv1alpha1.EnvVarSource{
								SecretKeyRef: kontextv1alpha1.SecretKeySelector{
									Name: "runtime-auth",
									Key:  "token",
								},
							},
						},
					},
					Schedule: &kontextv1alpha1.ScheduleSpec{Expression: "0 * * * *"},
					Backoff:  &kontextv1alpha1.BackoffSpec{InitialSeconds: 3, MaxSeconds: 30},
				},
			},
			runName: "full-agent-7",
			goal:    "concrete resolved goal",
			want: &kontextv1alpha1.AgentRun{
				ObjectMeta: ownedRunMetadata(
					"full-agent",
					"agents",
					"full-agent-7",
					types.UID("full-agent-uid"),
				),
				Spec: kontextv1alpha1.AgentRunSpec{
					AgentRef: &kontextv1alpha1.AgentRef{Name: "full-agent"},
					Goal:     "concrete resolved goal",
					Provider: "openai",
					Model:    "gpt-test",
					Tools:    []string{"shell", "read_knowledge"},
					Budget: &kontextv1alpha1.BudgetSpec{
						Tokens:    ptr(int32(1234)),
						Wallclock: "5m",
						Dollars:   ptr(2.5),
					},
					SecretRef:             &kontextv1alpha1.SecretRef{Name: "provider-auth"},
					KnowledgeConfigMapRef: &kontextv1alpha1.ConfigMapRef{Name: "knowledge"},
					ServiceAccountName:    "agent-runner",
					Runtime: kontextv1alpha1.RuntimeSpec{
						Image:   "example/runtime:v1",
						Command: []string{"/runtime"},
						Args:    []string{"serve", "--json"},
						Result: &kontextv1alpha1.RuntimeResultSpec{
							Source: kontextv1alpha1.ResultSourceStdout,
							Format: kontextv1alpha1.ResultFormatKontextEnvelope,
						},
						SecurityContext: &kontextv1alpha1.RuntimeSecurityContext{
							AllowPrivilegeEscalation: ptr(false),
							ReadOnlyRootFilesystem:   ptr(true),
							RunAsNonRoot:             ptr(true),
							Capabilities: &kontextv1alpha1.RuntimeCapabilities{
								Drop: []string{"ALL", "NET_RAW"},
							},
							SeccompProfile: &kontextv1alpha1.RuntimeSeccompProfile{
								Type:             "Localhost",
								LocalhostProfile: "profiles/runtime.json",
							},
						},
					},
					Env: []kontextv1alpha1.EnvVar{
						{Name: "LITERAL", Value: ptr("value")},
						{
							Name: "SECRET",
							ValueFrom: &kontextv1alpha1.EnvVarSource{
								SecretKeyRef: kontextv1alpha1.SecretKeySelector{
									Name: "runtime-auth",
									Key:  "token",
								},
							},
						},
					},
				},
			},
			mutate: mutateFullAgent,
		},
		{
			name: "nil optional pointers and slices",
			agent: &kontextv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-agent",
					Namespace: "agents",
					UID:       types.UID("nil-agent-uid"),
				},
				Spec: kontextv1alpha1.AgentSpec{
					Provider: "",
					Model:    "default-provider-model",
					Runtime:  kontextv1alpha1.RuntimeSpec{Image: "example/runtime:minimal"},
				},
			},
			runName: "nil-agent-1",
			goal:    "minimal goal",
			want: &kontextv1alpha1.AgentRun{
				ObjectMeta: ownedRunMetadata(
					"nil-agent",
					"agents",
					"nil-agent-1",
					types.UID("nil-agent-uid"),
				),
				Spec: kontextv1alpha1.AgentRunSpec{
					AgentRef: &kontextv1alpha1.AgentRef{Name: "nil-agent"},
					Goal:     "minimal goal",
					Provider: "anthropic",
					Model:    "default-provider-model",
					Runtime:  kontextv1alpha1.RuntimeSpec{Image: "example/runtime:minimal"},
				},
			},
			mutate: func(agent *kontextv1alpha1.Agent) {
				agent.Spec.Tools = []string{"new-tool"}
				agent.Spec.Budget = &kontextv1alpha1.BudgetSpec{Tokens: ptr(int32(1))}
				agent.Spec.SecretRef = &kontextv1alpha1.SecretRef{Name: "new-secret"}
				agent.Spec.KnowledgeConfigMapRef = &kontextv1alpha1.ConfigMapRef{Name: "new-config"}
				agent.Spec.Runtime.Command = []string{"/new-command"}
				agent.Spec.Runtime.Args = []string{"new-arg"}
				agent.Spec.Runtime.Result = &kontextv1alpha1.RuntimeResultSpec{}
				agent.Spec.Runtime.SecurityContext = &kontextv1alpha1.RuntimeSecurityContext{}
				agent.Spec.Env = []kontextv1alpha1.EnvVar{{Name: "NEW", Value: ptr("new")}}
			},
		},
		{
			name: "allocated empty pointers and slices",
			agent: &kontextv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-agent",
					Namespace: "agents",
					UID:       types.UID("empty-agent-uid"),
				},
				Spec: kontextv1alpha1.AgentSpec{
					Provider:              "FAKE",
					Model:                 "empty-model",
					Tools:                 []string{},
					Budget:                &kontextv1alpha1.BudgetSpec{},
					SecretRef:             &kontextv1alpha1.SecretRef{},
					KnowledgeConfigMapRef: &kontextv1alpha1.ConfigMapRef{},
					Runtime: kontextv1alpha1.RuntimeSpec{
						Image:           "example/runtime:empty",
						Command:         []string{},
						Args:            []string{},
						Result:          &kontextv1alpha1.RuntimeResultSpec{},
						SecurityContext: &kontextv1alpha1.RuntimeSecurityContext{},
					},
					Env: []kontextv1alpha1.EnvVar{},
				},
			},
			runName: "empty-agent-1",
			goal:    "empty collections goal",
			want: &kontextv1alpha1.AgentRun{
				ObjectMeta: ownedRunMetadata(
					"empty-agent",
					"agents",
					"empty-agent-1",
					types.UID("empty-agent-uid"),
				),
				Spec: kontextv1alpha1.AgentRunSpec{
					AgentRef:              &kontextv1alpha1.AgentRef{Name: "empty-agent"},
					Goal:                  "empty collections goal",
					Provider:              "fake",
					Model:                 "empty-model",
					Tools:                 []string{},
					Budget:                &kontextv1alpha1.BudgetSpec{},
					SecretRef:             &kontextv1alpha1.SecretRef{},
					KnowledgeConfigMapRef: &kontextv1alpha1.ConfigMapRef{},
					Runtime: kontextv1alpha1.RuntimeSpec{
						Image:           "example/runtime:empty",
						Command:         []string{},
						Args:            []string{},
						Result:          &kontextv1alpha1.RuntimeResultSpec{},
						SecurityContext: &kontextv1alpha1.RuntimeSecurityContext{},
					},
					Env: []kontextv1alpha1.EnvVar{},
				},
			},
			mutate: func(agent *kontextv1alpha1.Agent) {
				agent.Spec.Tools = append(agent.Spec.Tools, "new-tool")
				agent.Spec.Budget.Tokens = ptr(int32(1))
				agent.Spec.SecretRef.Name = "new-secret"
				agent.Spec.KnowledgeConfigMapRef.Name = "new-config"
				agent.Spec.Runtime.Command = append(agent.Spec.Runtime.Command, "/new-command")
				agent.Spec.Runtime.Args = append(agent.Spec.Runtime.Args, "new-arg")
				agent.Spec.Runtime.Result.Source = kontextv1alpha1.ResultSourceStdout
				agent.Spec.Runtime.SecurityContext.RunAsNonRoot = ptr(true)
				agent.Spec.Env = append(agent.Spec.Env, kontextv1alpha1.EnvVar{
					Name:  "NEW",
					Value: ptr("new"),
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := runfactory.NewForAgent(test.agent, test.runName, test.goal, scheme)
			if err != nil {
				t.Fatalf("build AgentRun snapshot: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("unexpected AgentRun snapshot:\ngot:  %#v\nwant: %#v", got, test.want)
			}

			test.mutate(test.agent)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("AgentRun snapshot changed after source mutation:\ngot:  %#v\nwant: %#v", got, test.want)
			}
		})
	}
}

func TestSnapshotFieldCoverage(t *testing.T) {
	assertStructFields(t, reflect.TypeFor[kontextv1alpha1.AgentSpec](), []string{
		"Backoff",
		"Budget",
		"Env",
		"Goal",
		"GoalTemplate",
		"KnowledgeConfigMapRef",
		"Mode",
		"Model",
		"Provider",
		"Runtime",
		"Schedule",
		"SecretRef",
		"ServiceAccountName",
		"Tools",
	})
	assertStructFields(t, reflect.TypeFor[kontextv1alpha1.AgentRunSpec](), []string{
		"AgentRef",
		"Budget",
		"Env",
		"Goal",
		"KnowledgeConfigMapRef",
		"Model",
		"Provider",
		"Runtime",
		"SecretRef",
		"ServiceAccountName",
		"Tools",
	})
}

func assertStructFields(t *testing.T, structType reflect.Type, want []string) {
	t.Helper()
	got := make([]string, 0, structType.NumField())
	for i := range structType.NumField() {
		got = append(got, structType.Field(i).Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s fields changed; classify the new field in the snapshot factory: got %v want %v", structType, got, want)
	}
}

func ownedRunMetadata(agentName, namespace, runName string, uid types.UID) metav1.ObjectMeta {
	controller := true
	blockOwnerDeletion := true
	return metav1.ObjectMeta{
		Name:      runName,
		Namespace: namespace,
		Labels: map[string]string{
			podbuilder.LabelAgentName: agentName,
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion:         kontextv1alpha1.GroupVersion.String(),
			Kind:               "Agent",
			Name:               agentName,
			UID:                uid,
			Controller:         &controller,
			BlockOwnerDeletion: &blockOwnerDeletion,
		}},
	}
}

func mutateFullAgent(agent *kontextv1alpha1.Agent) {
	agent.Name = "changed-agent"
	agent.Namespace = "changed-namespace"
	agent.Spec.Provider = "anthropic"
	agent.Spec.Model = "changed-model"
	agent.Spec.Tools[0] = "changed-tool"
	*agent.Spec.Budget.Tokens = 999
	agent.Spec.Budget.Wallclock = "1s"
	*agent.Spec.Budget.Dollars = 99
	agent.Spec.SecretRef.Name = "changed-secret"
	agent.Spec.KnowledgeConfigMapRef.Name = "changed-config"
	agent.Spec.ServiceAccountName = "changed-service-account"
	agent.Spec.Runtime.Image = "changed/image"
	agent.Spec.Runtime.Command[0] = "/changed-command"
	agent.Spec.Runtime.Args[0] = "changed-arg"
	agent.Spec.Runtime.Result.Format = kontextv1alpha1.ResultFormatLastLine
	*agent.Spec.Runtime.SecurityContext.AllowPrivilegeEscalation = true
	*agent.Spec.Runtime.SecurityContext.ReadOnlyRootFilesystem = false
	*agent.Spec.Runtime.SecurityContext.RunAsNonRoot = false
	agent.Spec.Runtime.SecurityContext.Capabilities.Drop[0] = "CHANGED"
	agent.Spec.Runtime.SecurityContext.SeccompProfile.Type = "RuntimeDefault"
	agent.Spec.Runtime.SecurityContext.SeccompProfile.LocalhostProfile = ""
	*agent.Spec.Env[0].Value = "changed-value"
	agent.Spec.Env[1].ValueFrom.SecretKeyRef.Name = "changed-runtime-auth"
	agent.Spec.Env[1].ValueFrom.SecretKeyRef.Key = "changed-token"
}

func ptr[T any](value T) *T {
	return &value
}
