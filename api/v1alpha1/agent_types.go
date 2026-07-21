package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	AgentModeTask      = "Task"
	AgentModeService   = "Service"
	AgentModeScheduled = "Scheduled"

	ConcurrencyPolicyAllow  ConcurrencyPolicy = "Allow"
	ConcurrencyPolicyForbid ConcurrencyPolicy = "Forbid"

	ResultSourceStdout          ResultSource = "Stdout"
	ResultFormatLastLine        ResultFormat = "LastLine"
	ResultFormatKontextEnvelope ResultFormat = "KontextEnvelope"
)

// AgentSpec defines the desired state of Agent.
// +kubebuilder:validation:XValidation:rule="self.mode == 'Task' ? has(self.goal) != has(self.goalTemplate) : has(self.goal) && !has(self.goalTemplate)",message="Task agents require exactly one of goal or goalTemplate; Service and Scheduled agents require goal and forbid goalTemplate"
// +kubebuilder:validation:XValidation:rule="self.mode == 'Scheduled' ? has(self.schedule) : !has(self.schedule)",message="schedule is required only for Scheduled agents"
// +kubebuilder:validation:XValidation:rule="self.mode == 'Service' || !has(self.backoff)",message="backoff is only valid for Service agents"
type AgentSpec struct {
	Mode AgentMode `json:"mode"`

	Runtime RuntimeSpec `json:"runtime"`

	// +kubebuilder:validation:MinLength=1
	Goal string `json:"goal,omitempty"`
	// +kubebuilder:validation:MinLength=1
	GoalTemplate string `json:"goalTemplate,omitempty"`

	Provider string `json:"provider,omitempty"`
	// +kubebuilder:validation:MinLength=1
	Model string   `json:"model"`
	Tools []string `json:"tools,omitempty"`

	Budget *BudgetSpec `json:"budget,omitempty"`

	SecretRef *SecretRef `json:"secretRef,omitempty"`

	KnowledgeConfigMapRef *ConfigMapRef `json:"knowledgeConfigMapRef,omitempty"`

	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	Env []EnvVar `json:"env,omitempty"`

	Schedule *ScheduleSpec `json:"schedule,omitempty"`
	Backoff  *BackoffSpec  `json:"backoff,omitempty"`
}

// AgentMode defines how an Agent behaves.
// +kubebuilder:validation:Enum=Task;Service;Scheduled
type AgentMode string

// ScheduleSpec controls Scheduled-mode run creation.
// +kubebuilder:validation:XValidation:rule="!self.expression.matches('(^|[[:space:]])(CRON_TZ|TZ)=')",message="time zones must be configured with schedule.timeZone, not inside schedule.expression"
type ScheduleSpec struct {
	// Expression is a standard five-field cron expression.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^(\S+\s+){4}\S+$`
	Expression string `json:"expression"`

	// +kubebuilder:default="Etc/UTC"
	TimeZone string `json:"timeZone,omitempty"`

	// +kubebuilder:default=Forbid
	ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=0
	StartingDeadlineSeconds *int64 `json:"startingDeadlineSeconds,omitempty"`

	// +kubebuilder:default=false
	Suspend bool `json:"suspend,omitempty"`

	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	SuccessfulRunsHistoryLimit *int32 `json:"successfulRunsHistoryLimit,omitempty"`

	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	FailedRunsHistoryLimit *int32 `json:"failedRunsHistoryLimit,omitempty"`
}

// ConcurrencyPolicy controls whether a due slot may overlap an active run.
// Replace is intentionally deferred.
// +kubebuilder:validation:Enum=Allow;Forbid
type ConcurrencyPolicy string

// RuntimeSpec describes the container image implementing the runtime contract.
// +kubebuilder:validation:XValidation:rule="!has(self.result) || (has(self.command) && size(self.command) > 0 && size(self.command[0]) > 0)",message="runtime.command must provide a non-empty executable when runtime.result is configured"
type RuntimeSpec struct {
	// +kubebuilder:validation:MinLength=1
	Image           string                  `json:"image"`
	Command         []string                `json:"command,omitempty"`
	Args            []string                `json:"args,omitempty"`
	Result          *RuntimeResultSpec      `json:"result,omitempty"`
	SecurityContext *RuntimeSecurityContext `json:"securityContext,omitempty"`
}

// RuntimeSecurityContext exposes the portable container hardening fields used
// by Kontext examples without allowing privilege grants through this API.
// +kubebuilder:validation:XValidation:rule="!has(self.allowPrivilegeEscalation) || self.allowPrivilegeEscalation == false",message="allowPrivilegeEscalation may only be false"
// +kubebuilder:validation:XValidation:rule="!has(self.runAsNonRoot) || self.runAsNonRoot == true",message="runAsNonRoot may only be true"
type RuntimeSecurityContext struct {
	AllowPrivilegeEscalation *bool                  `json:"allowPrivilegeEscalation,omitempty"`
	ReadOnlyRootFilesystem   *bool                  `json:"readOnlyRootFilesystem,omitempty"`
	RunAsNonRoot             *bool                  `json:"runAsNonRoot,omitempty"`
	Capabilities             *RuntimeCapabilities   `json:"capabilities,omitempty"`
	SeccompProfile           *RuntimeSeccompProfile `json:"seccompProfile,omitempty"`
}

type RuntimeCapabilities struct {
	Drop []string `json:"drop,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.type == 'Localhost' ? has(self.localhostProfile) && self.localhostProfile.size() > 0 && !self.localhostProfile.startsWith('/') && !self.localhostProfile.contains('..') : !has(self.localhostProfile)",message="localhostProfile is required as a safe relative path only for Localhost seccomp profiles"
type RuntimeSeccompProfile struct {
	// +kubebuilder:validation:Enum=RuntimeDefault;Localhost
	Type string `json:"type"`

	LocalhostProfile string `json:"localhostProfile,omitempty"`
}

// RuntimeResultSpec opts an existing runtime image into result capture.
type RuntimeResultSpec struct {
	Source ResultSource `json:"source"`
	Format ResultFormat `json:"format"`
}

// ResultSource identifies the stream used to extract a runtime result.
// +kubebuilder:validation:Enum=Stdout
type ResultSource string

// ResultFormat identifies how the reporter extracts a result from stdout.
// +kubebuilder:validation:Enum=LastLine;KontextEnvelope
type ResultFormat string

// BudgetSpec limits resource consumption for an agent run.
// +kubebuilder:validation:XValidation:rule="!has(self.wallclock) || self.wallclock.size() == 0 || duration(self.wallclock) > duration('0s')",message="wallclock must be empty or a positive duration"
type BudgetSpec struct {
	// +kubebuilder:validation:Minimum=1
	Tokens *int32 `json:"tokens,omitempty"`

	Wallclock string `json:"wallclock,omitempty"`

	// +kubebuilder:validation:Minimum=0
	Dollars *float64 `json:"dollars,omitempty"`
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

// EnvVar is a name/value or Secret-backed value injected into the runtime Pod.
// +kubebuilder:validation:XValidation:rule="has(self.value) != has(self.valueFrom)",message="exactly one of value or valueFrom must be configured"
type EnvVar struct {
	Name      string        `json:"name"`
	Value     *string       `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	SecretKeyRef SecretKeySelector `json:"secretKeyRef"`
}

type SecretKeySelector struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
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

	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

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
