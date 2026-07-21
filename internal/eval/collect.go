package eval

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

type artifactRequirements struct {
	pod              bool
	logs             bool
	envelope         bool
	exitCode         bool
	statusResult     bool
	statusOutput     bool
	statusUsage      bool
	wantOutcome      bool
	wantErrorCode    bool
	wantModel        bool
	wantTurns        bool
	wantToolCalls    bool
	eventTypes       map[eventv1alpha1.Type]struct{}
	eventDetailTypes map[eventv1alpha1.Type]struct{}
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
	if requirements.wantOutcome {
		observation.Outcome = envelope.Outcome
	}
	if requirements.wantErrorCode && envelope.Error != nil {
		observation.Error = &EnvelopeErrorObservation{Code: boundedString(envelope.Error.Code, 4096)}
	}
	if requirements.wantModel || requirements.wantTurns || requirements.wantToolCalls {
		observation.Execution = &EnvelopeExecutionObservation{}
		if envelope.Execution != nil {
			if requirements.wantModel {
				observation.Execution.Model = boundedString(envelope.Execution.Model, 4096)
			}
			if requirements.wantTurns {
				observation.Execution.Turns = envelope.Execution.Turns
			}
			if requirements.wantToolCalls {
				observation.Execution.ToolCalls = envelope.Execution.ToolCalls
			}
		}
	}
	return observation
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
