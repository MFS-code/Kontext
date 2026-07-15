package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	Name string `json:"name"`
}

// AgentRunSpec defines the desired state of AgentRun.
type AgentRunSpec struct {
	AgentRef *AgentRef `json:"agentRef,omitempty"`

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

// UsageStatus records consumption for a completed run.
type UsageStatus struct {
	Tokens  int32   `json:"tokens,omitempty"`
	Dollars float64 `json:"dollars,omitempty"`
}

// AgentRunStatus defines the observed state of AgentRun.
type AgentRunStatus struct {
	Phase AgentRunPhase `json:"phase,omitempty"`

	PodName string `json:"podName,omitempty"`
	Result  string `json:"result,omitempty"`

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
