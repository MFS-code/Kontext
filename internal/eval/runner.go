package eval

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

type caseExecution struct {
	record       *Record
	item         Case
	requirements artifactRequirements
	run          *kontextv1alpha1.AgentRun
	pod          *corev1.Pod
	created      bool
}

func (runner Runner) RunSuite(ctx context.Context, suite EvalSuite) []Record {
	runner.Options = normalizeRunnerOptions(runner.Options, suite.Spec.Defaults)
	records := make([]Record, 0, len(suite.Spec.Cases))
	for _, item := range suite.Spec.Cases {
		records = append(records, runner.runCase(ctx, suite, item))
	}
	return records
}

func (runner Runner) runCase(ctx context.Context, suite EvalSuite, item Case) Record {
	now := runner.Options.Now
	invocation := runner.Options.InvocationID
	namespace := runner.Options.Namespace
	name := NameForCase(suite.Metadata.Name, item.ID, invocation)
	record := Record{
		APIVersion:  APIVersion,
		Kind:        RecordKind,
		Suite:       suite.Metadata.Name,
		CaseID:      item.ID,
		Description: item.Description,
		Run:         RunRef{Namespace: namespace, Name: name},
		StartedAt:   now().UTC(),
	}
	execution := caseExecution{record: &record, item: item}
	ownershipLabels := map[string]string{
		labelManagedBy:  "kontext-eval",
		labelEvalSuite:  labelValue(suite.Metadata.Name),
		labelEvalCase:   labelValue(item.ID),
		labelInvocation: labelValue(invocation),
	}
	requirements, err := requirementsForGraders(item.Graders)
	if err == nil {
		err = requirementsForSuiteAssertions(suite.Spec.Assertions, item.ID, &requirements)
		if err != nil {
			err = fmt.Errorf("resolve suite assertion requirements: %w", err)
		}
	}
	execution.requirements = requirements
	if err != nil {
		record.CollectionErrors = append(record.CollectionErrors, err.Error())
		return runner.finalize(ctx, &execution, now)
	}
	run := newAgentRun(name, namespace, item.AgentRun, ownershipLabels)
	execution.run = run
	caseCtx, cancel := context.WithTimeout(ctx, item.Timeout.Duration)
	defer cancel()
	if err := runner.Client.Create(caseCtx, run); err != nil {
		record.CollectionErrors = append(record.CollectionErrors, fmt.Sprintf("create AgentRun: %v", err))
		if apierrors.IsAlreadyExists(err) {
			record.CollectionErrors = append(
				record.CollectionErrors,
				"AgentRun name collision was left untouched",
			)
		} else if !runner.Options.KeepRuns {
			record.CollectionErrors = append(
				record.CollectionErrors,
				runner.cleanupCreateFailure(caseCtx, types.NamespacedName{
					Namespace: namespace,
					Name:      name,
				}, ownershipLabels)...,
			)
		}
		return runner.finalize(caseCtx, &execution, now)
	}
	execution.created = true

	terminalRun, waitErr := runner.waitForTerminal(caseCtx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	})
	if waitErr != nil {
		record.CollectionErrors = append(record.CollectionErrors, fmt.Sprintf("wait for terminal AgentRun: %v", waitErr))
	}
	if terminalRun == nil {
		terminalRun = run
	}
	record.TerminalPhase = terminalRun.Status.Phase
	record.Run.PodName = terminalRun.Status.PodName
	if requirements.statusResult {
		record.StatusResult = terminalRun.Status.Result
	}
	if requirements.statusUsage {
		record.StatusUsage = terminalRun.Status.Usage
	}
	if requirements.statusOutput && terminalRun.Status.Output != nil {
		record.StatusOutput = &StatusOutput{
			MediaType: terminalRun.Status.Output.MediaType,
			Value:     append([]byte(nil), terminalRun.Status.Output.Value.Raw...),
		}
	}

	execution.run = terminalRun
	if requirements.pod {
		if terminalRun.Status.PodName == "" {
			record.CollectionErrors = append(record.CollectionErrors, "required runtime Pod name was not recorded")
		} else {
			execution.pod = &corev1.Pod{}
			if err := runner.getWithRetry(caseCtx, types.NamespacedName{
				Namespace: namespace,
				Name:      terminalRun.Status.PodName,
			}, execution.pod); err != nil {
				record.CollectionErrors = append(record.CollectionErrors, fmt.Sprintf("get required runtime Pod: %v", err))
				execution.pod = nil
			}
		}
	}
	return runner.finalize(caseCtx, &execution, now)
}

func (runner Runner) finalize(
	ctx context.Context,
	execution *caseExecution,
	now func() time.Time,
) Record {
	record := execution.record
	collectionCtx := ctx
	cancelCollection := func() {}
	if ctx.Err() != nil {
		collectionCtx, cancelCollection = context.WithTimeout(
			context.WithoutCancel(ctx),
			5*time.Second,
		)
	}
	defer cancelCollection()

	runner.collectPodArtifacts(collectionCtx, execution)
	record.CompletedAt = now().UTC()
	record.DurationMillis = record.CompletedAt.Sub(record.StartedAt).Milliseconds()
	GradeRecord(record, execution.item.Graders)

	if execution.created && runner.Options.Judge != nil {
		judgeResult, err := runner.Options.Judge.Evaluate(collectionCtx, observationFor(*record))
		if err != nil {
			record.Judge = &JudgeResult{Configured: true, Error: boundedMessage(err.Error())}
		} else {
			record.Judge = &judgeResult
		}
	}
	if err := ctx.Err(); err != nil && !containsCollectionError(record.CollectionErrors, err.Error()) {
		record.CollectionErrors = append(record.CollectionErrors, fmt.Sprintf("case context ended: %v", err))
	}

	if execution.created && !runner.Options.KeepRuns {
		target := execution.run
		if target == nil {
			target = &kontextv1alpha1.AgentRun{}
			target.Namespace = record.Run.Namespace
			target.Name = record.Run.Name
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := runner.Client.Delete(cleanupCtx, target)
		cancel()
		if err != nil && !apierrors.IsNotFound(err) {
			record.CollectionErrors = append(record.CollectionErrors, fmt.Sprintf("cleanup AgentRun: %v", err))
		}
	}
	record.Pass = len(record.CollectionErrors) == 0 && gradesPass(record.Grades)
	if record.Judge != nil {
		record.Pass = record.Pass && record.Judge.Error == "" && record.Judge.Pass
	}
	return *record
}

func (runner Runner) collectPodArtifacts(ctx context.Context, execution *caseExecution) {
	record := execution.record
	requirements := execution.requirements
	pod := execution.pod
	if requirements.exitCode {
		record.PodExitCode = podExitCode(pod)
		if pod != nil && record.PodExitCode == nil {
			record.CollectionErrors = append(record.CollectionErrors, "required runtime container exit code was unavailable")
		}
	}
	if pod == nil {
		return
	}
	if requirements.envelope {
		terminated := runtimeTermination(pod)
		if terminated == nil {
			record.CollectionErrors = append(record.CollectionErrors, "required runtime termination message was unavailable")
		} else {
			envelope, err := resultv1alpha1.ParseVersioned(terminated.Message)
			if errors.Is(err, resultv1alpha1.ErrVersionedEnvelopeRequired) {
				record.CollectionErrors = append(
					record.CollectionErrors,
					"required versioned result envelope was absent from runtime termination message",
				)
			} else if err != nil {
				record.CollectionErrors = append(
					record.CollectionErrors,
					fmt.Sprintf("parse required runtime termination message: %v", err),
				)
			} else {
				record.Envelope = projectEnvelope(envelope, requirements)
			}
		}
	}
	if requirements.logs && runner.Logs == nil {
		record.CollectionErrors = append(record.CollectionErrors, "required runtime log fetcher is not configured")
	}
	if requirements.logs && runner.Logs != nil {
		logs, err := runner.Logs.Fetch(ctx, pod.Namespace, pod.Name, "runtime")
		if err != nil {
			record.CollectionErrors = append(record.CollectionErrors, fmt.Sprintf("fetch required runtime logs: %v", err))
		} else {
			parsed := ParseLogs(
				logs.Data,
				logs.Truncated,
				requirements.eventTypes,
				requirements.eventDetailTypes,
			)
			record.Events = parsed.Events
			record.CollectionErrors = append(record.CollectionErrors, parsed.Errors...)
		}
	}
}

func (runner Runner) waitForTerminal(
	ctx context.Context,
	key types.NamespacedName,
) (*kontextv1alpha1.AgentRun, error) {
	interval := runner.Options.PollInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var latest *kontextv1alpha1.AgentRun
	for {
		current := &kontextv1alpha1.AgentRun{}
		if err := runner.getWithRetry(ctx, key, current); err != nil {
			if !apierrors.IsNotFound(err) {
				return latest, err
			}
		} else {
			latest = current
			if terminalPhase(current.Status.Phase) {
				return current, nil
			}
		}
		select {
		case <-ctx.Done():
			return latest, ctx.Err()
		case <-ticker.C:
		}
	}
}

func terminalPhase(phase kontextv1alpha1.AgentRunPhase) bool {
	switch phase {
	case kontextv1alpha1.AgentRunPhaseSucceeded,
		kontextv1alpha1.AgentRunPhaseFailed,
		kontextv1alpha1.AgentRunPhaseBudgetExceeded:
		return true
	default:
		return false
	}
}
