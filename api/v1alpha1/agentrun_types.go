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

// AgentRunSpec defines the desired state of AgentRun. Persisted specs are
// always complete. The CREATE mutating webhook decodes sparse referenced
// requests into this type and resolves required execution fields before
// API-server schema validation and persistence.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="AgentRun spec is immutable"
// +kubebuilder:validation:XValidation:rule="has(self.agentRef) || !has(self.parameters)",message="parameters require agentRef"
// +kubebuilder:validation:XValidation:rule="has(self.agentRef) || !has(self.delivery)",message="delivery requires agentRef"
// +kubebuilder:validation:XValidation:rule="!has(self.delivery) || (has(self.runtime.delivery) && self.delivery.port == self.runtime.delivery.port)",message="delivery must match runtime.delivery in the execution snapshot"
// +kubebuilder:validation:XValidation:rule="has(self.goal) && has(self.model) && has(self.runtime)",message="persisted AgentRun spec must contain a complete execution snapshot"
type AgentRunSpec struct {
	AgentRef *AgentRef `json:"agentRef,omitempty"`

	// Parameters are retained with a resolved invocation snapshot for auditability.
	Parameters map[string]string `json:"parameters,omitempty"`

	// Delivery marks a run that executes through a standing Service runtime
	// instead of owning a Pod.
	Delivery *AgentRunDeliverySpec `json:"delivery,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Goal string `json:"goal"`

	Provider string `json:"provider,omitempty"`
	// +kubebuilder:validation:MinLength=1
	Model string   `json:"model"`
	Tools []string `json:"tools,omitempty"`

	Budget *BudgetSpec `json:"budget,omitempty"`

	SecretRef *SecretRef `json:"secretRef,omitempty"`

	KnowledgeConfigMapRef *ConfigMapRef `json:"knowledgeConfigMapRef,omitempty"`

	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	Runtime RuntimeSpec `json:"runtime"`

	Env []EnvVar `json:"env,omitempty"`
}

// AgentRunDeliverySpec is the immutable warm-delivery target snapshot.
type AgentRunDeliverySpec struct {
	// Port is copied from the referenced Service Agent's runtime contract.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
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

// IsTerminal reports whether the phase represents a finished AgentRun.
func (p AgentRunPhase) IsTerminal() bool {
	switch p {
	case AgentRunPhaseSucceeded, AgentRunPhaseFailed, AgentRunPhaseBudgetExceeded:
		return true
	default:
		return false
	}
}

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
