package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	AgentModeTask      = "Task"
	AgentModeService   = "Service"
	AgentModeScheduled = "Scheduled"
)

// AgentSpec defines the desired state of Agent.
type AgentSpec struct {
	Mode AgentMode `json:"mode"`

	Runtime RuntimeSpec `json:"runtime"`

	Goal         string `json:"goal,omitempty"`
	GoalTemplate string `json:"goalTemplate,omitempty"`

	Provider string   `json:"provider,omitempty"`
	Model    string   `json:"model"`
	Tools    []string `json:"tools,omitempty"`

	Budget *BudgetSpec `json:"budget,omitempty"`

	SecretRef *SecretRef `json:"secretRef,omitempty"`

	KnowledgeConfigMapRef *ConfigMapRef `json:"knowledgeConfigMapRef,omitempty"`

	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	Env []EnvVar `json:"env,omitempty"`

	Schedule string       `json:"schedule,omitempty"`
	Backoff  *BackoffSpec `json:"backoff,omitempty"`
}

// AgentMode defines how an Agent behaves.
// +kubebuilder:validation:Enum=Task;Service;Scheduled
type AgentMode string

// RuntimeSpec describes the container image implementing the runtime contract.
type RuntimeSpec struct {
	// +kubebuilder:validation:MinLength=1
	Image   string   `json:"image"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// BudgetSpec limits resource consumption for an agent run.
type BudgetSpec struct {
	Tokens    *int32   `json:"tokens,omitempty"`
	Wallclock string   `json:"wallclock,omitempty"`
	Dollars   *float64 `json:"dollars,omitempty"`
}

// SecretRef references a Kubernetes Secret with provider credentials.
type SecretRef struct {
	Name string `json:"name"`
}

// ConfigMapRef references a Kubernetes ConfigMap mounted into the runtime Pod.
type ConfigMapRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// EnvVar is a name/value pair injected into the runtime Pod.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// BackoffSpec controls Service-mode re-cast delays.
type BackoffSpec struct {
	InitialSeconds int32 `json:"initialSeconds,omitempty"`
	MaxSeconds     int32 `json:"maxSeconds,omitempty"`
}

// AgentStatus defines the observed state of Agent.
type AgentStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	CurrentRunName string `json:"currentRunName,omitempty"`
	LastRunName    string `json:"lastRunName,omitempty"`
	RunsCreated    int32  `json:"runsCreated,omitempty"`
	Restarts       int32  `json:"restarts,omitempty"`

	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ag
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="CurrentRun",type=string,JSONPath=`.status.currentRunName`
// +kubebuilder:printcolumn:name="Restarts",type=integer,JSONPath=`.status.restarts`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Agent is the Schema for the agents API.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent.
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Agent{}, &AgentList{})
}
