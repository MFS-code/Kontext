// Package runfactory builds immutable AgentRun snapshots from Agent definitions.
package runfactory

import (
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/runtimepolicy"
)

// ResolutionErrorCode identifies a stable Task resolution failure class.
type ResolutionErrorCode string

const (
	ErrorMissingAgent      ResolutionErrorCode = "MissingAgent"
	ErrorWrongMode         ResolutionErrorCode = "WrongMode"
	ErrorInvalidTemplate   ResolutionErrorCode = "InvalidTemplate"
	ErrorMissingParameters ResolutionErrorCode = "MissingParameters"
	ErrorUnusedParameters  ResolutionErrorCode = "UnusedParameters"
	ErrorConflictingFields ResolutionErrorCode = "ConflictingFields"
)

// ResolutionError is returned for every rejected Task invocation.
type ResolutionError struct {
	Code      ResolutionErrorCode
	AgentName string
	Mode      kontextv1alpha1.AgentMode
	Names     []string
	Detail    string
}

func (e *ResolutionError) Error() string {
	switch e.Code {
	case ErrorMissingAgent:
		return fmt.Sprintf("Task resolution failed [%s]: Agent %q was not found", e.Code, e.AgentName)
	case ErrorWrongMode:
		return fmt.Sprintf("Task resolution failed [%s]: Agent %q has mode %q", e.Code, e.AgentName, e.Mode)
	case ErrorInvalidTemplate:
		return fmt.Sprintf("Task resolution failed [%s]: %s", e.Code, e.Detail)
	case ErrorMissingParameters:
		return fmt.Sprintf("Task resolution failed [%s]: missing parameters: %s", e.Code, strings.Join(e.Names, ", "))
	case ErrorUnusedParameters:
		if len(e.Names) == 0 {
			return fmt.Sprintf("Task resolution failed [%s]: static goals do not accept parameters", e.Code)
		}
		return fmt.Sprintf("Task resolution failed [%s]: unused parameters: %s", e.Code, strings.Join(e.Names, ", "))
	case ErrorConflictingFields:
		return fmt.Sprintf("Task resolution failed [%s]: invocation supplies locked fields: %s", e.Code, strings.Join(e.Names, ", "))
	default:
		return fmt.Sprintf("Task resolution failed [%s]", e.Code)
	}
}

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

// ResolveTask resolves a sparse user invocation against a Task Agent. It is
// pure: neither input is mutated, and the returned run shares no execution
// data with either input.
func ResolveTask(
	agent *kontextv1alpha1.Agent,
	invocation *kontextv1alpha1.AgentRun,
	scheme *runtime.Scheme,
) (*kontextv1alpha1.AgentRun, error) {
	referenceName := ""
	if invocation != nil && invocation.Spec.AgentRef != nil {
		referenceName = invocation.Spec.AgentRef.Name
	}
	if agent == nil || referenceName == "" || referenceName != agent.Name ||
		(invocation.Namespace != "" && invocation.Namespace != agent.Namespace) {
		return nil, &ResolutionError{Code: ErrorMissingAgent, AgentName: referenceName}
	}
	if agent.Spec.Mode != kontextv1alpha1.AgentModeTask {
		return nil, &ResolutionError{
			Code:      ErrorWrongMode,
			AgentName: agent.Name,
			Mode:      agent.Spec.Mode,
		}
	}

	if fields := lockedInvocationFields(invocation.Spec); len(fields) > 0 {
		return nil, &ResolutionError{Code: ErrorConflictingFields, Names: fields}
	}

	goal, err := resolveGoal(agent.Spec, invocation.Spec.Parameters)
	if err != nil {
		return nil, err
	}

	resolved, err := NewForAgent(agent, invocation.Name, goal, scheme)
	if err != nil {
		return nil, err
	}
	resolved.TypeMeta = invocation.TypeMeta
	resolved.Annotations = maps.Clone(invocation.Annotations)
	resolved.Finalizers = append([]string(nil), invocation.Finalizers...)
	for key, value := range invocation.Labels {
		if resolved.Labels == nil {
			resolved.Labels = make(map[string]string)
		}
		resolved.Labels[key] = value
	}
	resolved.Labels[podbuilder.LabelAgentName] = agent.Name
	resolved.Spec.Parameters = maps.Clone(invocation.Spec.Parameters)
	return resolved, nil
}

func lockedInvocationFields(spec kontextv1alpha1.AgentRunSpec) []string {
	fields := make([]string, 0, 10)
	if spec.Goal != "" {
		fields = append(fields, "goal")
	}
	if spec.Provider != "" {
		fields = append(fields, "provider")
	}
	if spec.Model != "" {
		fields = append(fields, "model")
	}
	if spec.Tools != nil {
		fields = append(fields, "tools")
	}
	if spec.Budget != nil {
		fields = append(fields, "budget")
	}
	if spec.SecretRef != nil {
		fields = append(fields, "secretRef")
	}
	if spec.KnowledgeConfigMapRef != nil {
		fields = append(fields, "knowledgeConfigMapRef")
	}
	if spec.ServiceAccountName != "" {
		fields = append(fields, "serviceAccountName")
	}
	if !reflect.DeepEqual(spec.Runtime, kontextv1alpha1.RuntimeSpec{}) {
		fields = append(fields, "runtime")
	}
	if spec.Env != nil {
		fields = append(fields, "env")
	}
	sort.Strings(fields)
	return fields
}

func resolveGoal(spec kontextv1alpha1.AgentSpec, parameters map[string]string) (string, error) {
	hasGoal := spec.Goal != ""
	hasTemplate := spec.GoalTemplate != ""
	if hasGoal == hasTemplate {
		return "", &ResolutionError{
			Code:   ErrorInvalidTemplate,
			Detail: "Task Agent must configure exactly one of goal or goalTemplate",
		}
	}
	if hasGoal {
		if parameters != nil {
			names := sortedMapKeys(parameters)
			return "", &ResolutionError{Code: ErrorUnusedParameters, Names: names}
		}
		return spec.Goal, nil
	}
	return interpolateGoal(spec.GoalTemplate, parameters)
}

func interpolateGoal(template string, parameters map[string]string) (string, error) {
	var rendered strings.Builder
	used := make(map[string]struct{})
	missing := make(map[string]struct{})

	for index := 0; index < len(template); {
		switch {
		case strings.HasPrefix(template[index:], "$${"):
			name, next, err := parsePlaceholder(template, index+1)
			if err != nil {
				return "", err
			}
			rendered.WriteString("${")
			rendered.WriteString(name)
			rendered.WriteByte('}')
			index = next
		case strings.HasPrefix(template[index:], "${"):
			name, next, err := parsePlaceholder(template, index)
			if err != nil {
				return "", err
			}
			value, ok := parameters[name]
			if !ok {
				missing[name] = struct{}{}
			} else {
				used[name] = struct{}{}
				rendered.WriteString(value)
			}
			index = next
		default:
			rendered.WriteByte(template[index])
			index++
		}
	}

	if len(missing) > 0 {
		return "", &ResolutionError{Code: ErrorMissingParameters, Names: sortedSetKeys(missing)}
	}
	unused := make(map[string]struct{})
	for name := range parameters {
		if _, ok := used[name]; !ok {
			unused[name] = struct{}{}
		}
	}
	if len(unused) > 0 {
		return "", &ResolutionError{Code: ErrorUnusedParameters, Names: sortedSetKeys(unused)}
	}
	return rendered.String(), nil
}

func parsePlaceholder(template string, start int) (string, int, error) {
	nameStart := start + 2
	endOffset := strings.IndexByte(template[nameStart:], '}')
	if endOffset < 0 {
		return "", 0, &ResolutionError{
			Code:   ErrorInvalidTemplate,
			Detail: fmt.Sprintf("unterminated placeholder at byte %d", start),
		}
	}
	end := nameStart + endOffset
	name := template[nameStart:end]
	if !validParameterName(name) {
		return "", 0, &ResolutionError{
			Code:   ErrorInvalidTemplate,
			Detail: fmt.Sprintf("invalid placeholder %q at byte %d", name, start),
		}
	}
	return name, end + 1, nil
}

func validParameterName(name string) bool {
	if name == "" || !asciiLetterOrUnderscore(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		if !asciiLetterOrUnderscore(name[index]) && (name[index] < '0' || name[index] > '9') {
			return false
		}
	}
	return true
}

func asciiLetterOrUnderscore(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
