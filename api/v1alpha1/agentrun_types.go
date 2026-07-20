package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	AgentRunPhasePending        = "Pending"
	AgentRunPhaseRunning        = "Running"
	AgentRunPhaseSucceeded      = "Succeeded"
	AgentRunPhaseFailed         = "Failed"
	AgentRunPhaseBudgetExceeded = "BudgetExceeded"
)

// AgentRef links an AgentRun to its owning Agent.
type AgentRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AgentRunSpec defines the desired state of AgentRun.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="AgentRun spec is immutable"
// +kubebuilder:validation:XValidation:rule="has(self.agentRef) || !has(self.parameters)",message="parameters require agentRef"
// +kubebuilder:validation:XValidation:rule="(has(self.goal) && has(self.model) && has(self.runtime)) || (has(self.agentRef) && !has(self.goal) && !has(self.provider) && !has(self.model) && !has(self.tools) && !has(self.budget) && !has(self.secretRef) && !has(self.knowledgeConfigMapRef) && !has(self.serviceAccountName) && !has(self.runtime) && !has(self.env))",message="AgentRun must be a complete execution spec or a sparse invocation containing only agentRef and parameters"
type AgentRunSpec struct {
	AgentRef *AgentRef `json:"agentRef,omitempty"`

	// Parameters are retained with a resolved Task snapshot for auditability.
	Parameters map[string]string `json:"parameters,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Goal string `json:"goal,omitempty"`

	Provider string `json:"provider,omitempty"`
	// +kubebuilder:validation:MinLength=1
	Model string   `json:"model,omitempty"`
	Tools []string `json:"tools,omitempty"`

	Budget *BudgetSpec `json:"budget,omitempty"`

	SecretRef *SecretRef `json:"secretRef,omitempty"`

	KnowledgeConfigMapRef *ConfigMapRef `json:"knowledgeConfigMapRef,omitempty"`

	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	Runtime RuntimeSpec `json:"runtime,omitempty,omitzero"`

	Env []EnvVar `json:"env,omitempty"`
}

// OutputStatus preserves the runtime's structured terminal output.
type OutputStatus struct {
	MediaType string `json:"mediaType"`

	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Value runtime.RawExtension `json:"value"`
}

// UsageStatus records measured consumption for a completed run. Pointer fields
// distinguish a measured zero from a metric the provider did not report.
type UsageStatus struct {
	Tokens       *int64   `json:"tokens,omitempty"`
	InputTokens  *int64   `json:"inputTokens,omitempty"`
	OutputTokens *int64   `json:"outputTokens,omitempty"`
	Dollars      *float64 `json:"dollars,omitempty"`
}

// AgentRunStatus defines the observed state of AgentRun.
type AgentRunStatus struct {
	Phase AgentRunPhase `json:"phase,omitempty"`

	PodName string        `json:"podName,omitempty"`
	Result  string        `json:"result,omitempty"`
	Output  *OutputStatus `json:"output,omitempty"`

	Usage *UsageStatus `json:"usage,omitempty"`

	StartTime      *metav1.Time `json:"startTime,omitempty"`
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	Message string `json:"message,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// AgentRunPhase is the lifecycle phase of an AgentRun.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;BudgetExceeded
type AgentRunPhase string

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ar
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.podName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentRun is the Schema for the agentruns API.
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRunSpec   `json:"spec,omitempty"`
	Status AgentRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentRunList contains a list of AgentRun.
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRun{}, &AgentRunList{})
}
