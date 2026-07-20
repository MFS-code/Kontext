package eval

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

type envelopeProjector func(resultv1alpha1.Envelope, *EnvelopeObservation)

type artifactRequirements struct {
	pod                bool
	logs               bool
	envelope           bool
	exitCode           bool
	statusResult       bool
	statusOutput       bool
	statusUsage        bool
	eventTypes         map[eventv1alpha1.Type]struct{}
	eventDetailTypes   map[eventv1alpha1.Type]struct{}
	envelopeProjectors []envelopeProjector
}

func requirementsForGraders(graders []Grader) (artifactRequirements, error) {
	requirements := artifactRequirements{
		eventTypes:       make(map[eventv1alpha1.Type]struct{}),
		eventDetailTypes: make(map[eventv1alpha1.Type]struct{}),
	}
	for _, grader := range graders {
		spec, err := graderSpecFor(grader.Type)
		if err != nil {
			return artifactRequirements{}, fmt.Errorf("resolve grader requirements: %w", err)
		}
		spec.requirements(grader, &requirements)
	}
	return requirements, nil
}

func projectEnvelope(
	envelope resultv1alpha1.Envelope,
	requirements artifactRequirements,
) *EnvelopeObservation {
	observation := &EnvelopeObservation{}
	for _, projector := range requirements.envelopeProjectors {
		projector(envelope, observation)
	}
	return observation
}

func projectEnvelopeOutcome(envelope resultv1alpha1.Envelope, observation *EnvelopeObservation) {
	observation.Outcome = envelope.Outcome
}

func projectEnvelopeError(envelope resultv1alpha1.Envelope, observation *EnvelopeObservation) {
	if envelope.Error != nil {
		observation.Error = &EnvelopeErrorObservation{Code: boundedString(envelope.Error.Code, 4096)}
	}
}

func projectEnvelopeModel(envelope resultv1alpha1.Envelope, observation *EnvelopeObservation) {
	execution := ensureEnvelopeExecution(observation)
	if envelope.Execution != nil {
		execution.Model = boundedString(envelope.Execution.Model, 4096)
	}
}

func projectEnvelopeTurns(envelope resultv1alpha1.Envelope, observation *EnvelopeObservation) {
	execution := ensureEnvelopeExecution(observation)
	if envelope.Execution != nil {
		execution.Turns = cloneInt32(envelope.Execution.Turns)
	}
}

func projectEnvelopeTools(envelope resultv1alpha1.Envelope, observation *EnvelopeObservation) {
	execution := ensureEnvelopeExecution(observation)
	if envelope.Execution != nil {
		execution.ToolCalls = cloneInt32(envelope.Execution.ToolCalls)
	}
}

func ensureEnvelopeExecution(observation *EnvelopeObservation) *EnvelopeExecutionObservation {
	if observation.Execution == nil {
		observation.Execution = &EnvelopeExecutionObservation{}
	}
	return observation.Execution
}

func cloneInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func containsCollectionError(errors []string, fragment string) bool {
	for _, message := range errors {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func runtimeTermination(pod *corev1.Pod) *corev1.ContainerStateTerminated {
	for index := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[index]
		if status.Name == "runtime" {
			return status.State.Terminated
		}
	}
	return nil
}
