package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

const (
	labelManagedBy  = "app.kubernetes.io/managed-by"
	labelEvalSuite  = "kontext.dev/eval-suite"
	labelEvalCase   = "kontext.dev/eval-case"
	labelInvocation = "kontext.dev/eval-invocation"
)

type RunnerOptions struct {
	Namespace    string
	KeepRuns     bool
	PollInterval time.Duration
	Judge        Judge
	Now          func() time.Time
	InvocationID string
}

type Runner struct {
	Client  client.Client
	Logs    LogFetcher
	Options RunnerOptions
}

func (runner Runner) RunSuite(ctx context.Context, suite EvalSuite) []Record {
	runner.Options = normalizeRunnerOptions(runner.Options, suite.Spec.Defaults)
	records := make([]Record, 0, len(suite.Spec.Cases))
	for _, item := range suite.Spec.Cases {
		records = append(records, runner.runCase(ctx, suite, item))
	}
	return records
}

func normalizeRunnerOptions(options RunnerOptions, defaults SuiteDefaults) RunnerOptions {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.InvocationID == "" {
		options.InvocationID = invocationID(options.Now())
	}
	if options.Namespace == "" {
		options.Namespace = defaults.Namespace
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 500 * time.Millisecond
	}
	return options
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
	ownershipLabels := map[string]string{
		labelManagedBy:  "kontext-eval",
		labelEvalSuite:  labelValue(suite.Metadata.Name),
		labelEvalCase:   labelValue(item.ID),
		labelInvocation: labelValue(invocation),
	}
	requirements, requirementsErr := requirementsForGraders(item.Graders)
	if requirementsErr != nil {
		record.CollectionErrors = append(record.CollectionErrors, requirementsErr.Error())
		return runner.finishRecord(
			ctx,
			&record,
			item,
			requirements,
			nil,
			nil,
			false,
			now,
		)
	}
	if err := requirementsForSuiteAssertions(
		suite.Spec.Assertions,
		item.ID,
		&requirements,
	); err != nil {
		record.CollectionErrors = append(
			record.CollectionErrors,
			fmt.Sprintf("resolve suite assertion requirements: %v", err),
		)
		return runner.finishRecord(
			ctx,
			&record,
			item,
			requirements,
			nil,
			nil,
			false,
			now,
		)
	}
	run := newAgentRun(name, namespace, item.AgentRun, ownershipLabels)
	caseCtx, cancel := context.WithTimeout(ctx, item.Timeout.Duration)
	defer cancel()
	created := false
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
		return runner.finishRecord(caseCtx, &record, item, requirements, nil, nil, created, now)
	}
	created = true

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

	var pod *corev1.Pod
	if !requirements.pod {
		return runner.finishRecord(caseCtx, &record, item, requirements, terminalRun, nil, created, now)
	}
	if terminalRun.Status.PodName == "" {
		record.CollectionErrors = append(record.CollectionErrors, "required runtime Pod name was not recorded")
	} else {
		pod = &corev1.Pod{}
		if err := runner.getWithRetry(caseCtx, types.NamespacedName{
			Namespace: namespace,
			Name:      terminalRun.Status.PodName,
		}, pod); err != nil {
			record.CollectionErrors = append(record.CollectionErrors, fmt.Sprintf("get required runtime Pod: %v", err))
			pod = nil
		}
	}
	return runner.finishRecord(caseCtx, &record, item, requirements, terminalRun, pod, created, now)
}

func (runner Runner) finishRecord(
	ctx context.Context,
	record *Record,
	item Case,
	requirements artifactRequirements,
	run *kontextv1alpha1.AgentRun,
	pod *corev1.Pod,
	created bool,
	now func() time.Time,
) Record {
	collectionCtx := ctx
	cancelCollection := func() {}
	if ctx.Err() != nil {
		collectionCtx, cancelCollection = context.WithTimeout(
			context.WithoutCancel(ctx),
			5*time.Second,
		)
	}
	defer cancelCollection()

	if requirements.exitCode {
		record.PodExitCode = podExitCode(pod)
		if pod != nil && record.PodExitCode == nil {
			record.CollectionErrors = append(record.CollectionErrors, "required runtime container exit code was unavailable")
		}
	}
	if pod != nil {
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
			logs, err := runner.Logs.Fetch(collectionCtx, pod.Namespace, pod.Name, "runtime")
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
	record.CompletedAt = now().UTC()
	record.DurationMillis = record.CompletedAt.Sub(record.StartedAt).Milliseconds()
	GradeRecord(record, item.Graders)

	if created && runner.Options.Judge != nil {
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

	if created && !runner.Options.KeepRuns {
		target := run
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

func invocationID(now time.Time) string {
	random := make([]byte, 3)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%x", now.UnixNano())
	}
	return fmt.Sprintf("%x-%s", now.Unix(), hex.EncodeToString(random))
}

func labelValue(value string) string {
	value = NameForCase(value, "", "")
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}
