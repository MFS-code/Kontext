package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	clientretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

const (
	labelManagedBy  = "app.kubernetes.io/managed-by"
	labelEvalSuite  = "kontext.dev/eval-suite"
	labelEvalCase   = "kontext.dev/eval-case"
	labelInvocation = "kontext.dev/eval-invocation"
)

type LogFetcher interface {
	Fetch(context.Context, string, string, string) (LogCollection, error)
}

type LogCollection struct {
	Data      []byte
	Truncated bool
}

const MaxLogTailLines int64 = MaxEventCount + 1

type PodLogStreamer func(
	context.Context,
	string,
	string,
	*corev1.PodLogOptions,
) (io.ReadCloser, error)

type KubernetesLogFetcher struct {
	Client kubernetes.Interface
	Stream PodLogStreamer
}

func (fetcher KubernetesLogFetcher) Fetch(
	ctx context.Context,
	namespace, podName, container string,
) (LogCollection, error) {
	tailLines := MaxLogTailLines
	limitBytes := int64(MaxLogBytes + 1)
	options := &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &tailLines,
		LimitBytes: &limitBytes,
	}
	streamLogs := fetcher.Stream
	if streamLogs == nil {
		streamLogs = func(
			ctx context.Context,
			namespace string,
			podName string,
			options *corev1.PodLogOptions,
		) (io.ReadCloser, error) {
			return fetcher.Client.CoreV1().Pods(namespace).GetLogs(podName, options).Stream(ctx)
		}
	}
	stream, err := streamLogs(ctx, namespace, podName, options)
	if err != nil {
		return LogCollection{}, err
	}
	defer stream.Close()
	tail := newBoundedTail(MaxLogBytes)
	if _, err := io.Copy(tail, stream); err != nil {
		return LogCollection{}, err
	}
	return LogCollection{
		Data:      append([]byte(nil), tail.Bytes()...),
		Truncated: tail.Truncated() || logLineCount(tail.Bytes()) >= MaxLogTailLines,
	}, nil
}

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
	run := newAgentRun(name, namespace, item.AgentRun, ownershipLabels)
	caseCtx, cancel := context.WithTimeout(ctx, item.Timeout.Duration)
	defer cancel()
	created := false
	if err := runner.Client.Create(caseCtx, run); err != nil {
		record.CollectionErrors = append(record.CollectionErrors, fmt.Sprintf("create AgentRun: %v", err))
		if !runner.Options.KeepRuns && classifyKubernetesError(err).ambiguousWrite {
			record.CollectionErrors = append(
				record.CollectionErrors,
				runner.cleanupAmbiguousCreate(caseCtx, types.NamespacedName{
					Namespace: namespace,
					Name:      name,
				}, ownershipLabels)...,
			)
		} else if apierrors.IsAlreadyExists(err) {
			record.CollectionErrors = append(
				record.CollectionErrors,
				"AgentRun name collision was left untouched",
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
	record.CompletedAt = now().UTC()
	record.DurationMillis = record.CompletedAt.Sub(record.StartedAt).Milliseconds()
	GradeRecord(record, item.Graders)

	if runner.Options.Judge != nil {
		judgeResult, err := runner.Options.Judge.Evaluate(ctx, observationFor(*record))
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

type kubernetesErrorClassification struct {
	retryableRead  bool
	ambiguousWrite bool
}

func classifyKubernetesError(err error) kubernetesErrorClassification {
	if apierrors.IsAlreadyExists(err) ||
		apierrors.IsBadRequest(err) ||
		apierrors.IsInvalid(err) ||
		apierrors.IsForbidden(err) ||
		apierrors.IsUnauthorized(err) ||
		apierrors.IsMethodNotSupported(err) ||
		apierrors.IsNotAcceptable(err) ||
		apierrors.IsUnsupportedMediaType(err) {
		return kubernetesErrorClassification{}
	}
	if apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return kubernetesErrorClassification{retryableRead: true, ambiguousWrite: true}
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return kubernetesErrorClassification{retryableRead: true, ambiguousWrite: true}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return kubernetesErrorClassification{retryableRead: true, ambiguousWrite: true}
	}
	return kubernetesErrorClassification{ambiguousWrite: true}
}

func (runner Runner) cleanupAmbiguousCreate(
	parent context.Context,
	key types.NamespacedName,
	expectedLabels map[string]string,
) []string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 300*time.Millisecond)
	defer cancel()
	var lastErr error
	deleteAttempted := false
	err := clientretry.OnError(wait.Backoff{
		Duration: 25 * time.Millisecond,
		Factor:   2,
		Steps:    8,
		Cap:      200 * time.Millisecond,
	}, func(err error) bool {
		return ctx.Err() == nil &&
			(apierrors.IsNotFound(err) || classifyKubernetesError(err).retryableRead)
	}, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		observed := &kontextv1alpha1.AgentRun{}
		err := runner.Client.Get(ctx, key, observed)
		if apierrors.IsNotFound(err) && deleteAttempted {
			return nil
		}
		if err == nil {
			if !hasExactEvaluatorOwnership(observed.Labels, expectedLabels) {
				return errors.New("ambiguous AgentRun exists without exact evaluator ownership; left untouched")
			}
			deleteAttempted = true
			if err := runner.Client.Delete(ctx, observed); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("cleanup ambiguous AgentRun: %w", err)
			}
			return nil
		}
		lastErr = err
		return err
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return []string{fmt.Sprintf(
			"probe ambiguous AgentRun after create error: %v (last read: %v)",
			ctx.Err(),
			lastErr,
		)}
	}
	return []string{err.Error()}
}

func hasExactEvaluatorOwnership(actual, expected map[string]string) bool {
	for _, name := range []string{
		labelManagedBy,
		labelEvalSuite,
		labelEvalCase,
		labelInvocation,
	} {
		if actual[name] == "" || actual[name] != expected[name] {
			return false
		}
	}
	return true
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

type boundedTail struct {
	data      []byte
	limit     int
	truncated bool
}

func newBoundedTail(limit int) *boundedTail {
	return &boundedTail{data: make([]byte, 0, limit), limit: limit}
}

func (tail *boundedTail) Write(data []byte) (int, error) {
	originalLength := len(data)
	if tail.limit <= 0 {
		tail.truncated = tail.truncated || len(data) > 0
		return originalLength, nil
	}
	if len(data) >= tail.limit {
		tail.data = append(tail.data[:0], data[len(data)-tail.limit:]...)
		tail.truncated = true
		return originalLength, nil
	}
	overflow := len(tail.data) + len(data) - tail.limit
	if overflow > 0 {
		copy(tail.data, tail.data[overflow:])
		tail.data = tail.data[:len(tail.data)-overflow]
		tail.truncated = true
	}
	tail.data = append(tail.data, data...)
	return originalLength, nil
}

func (tail *boundedTail) Bytes() []byte {
	return tail.data
}

func (tail *boundedTail) Truncated() bool {
	return tail.truncated
}

func logLineCount(data []byte) int64 {
	if len(data) == 0 {
		return 0
	}
	count := int64(1)
	for _, value := range data {
		if value == '\n' {
			count++
		}
	}
	if data[len(data)-1] == '\n' {
		count--
	}
	return count
}

func (runner Runner) getWithRetry(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
) error {
	return clientretry.OnError(wait.Backoff{
		Duration: 25 * time.Millisecond,
		Factor:   2,
		Steps:    10_000,
		Cap:      500 * time.Millisecond,
	}, func(err error) bool {
		return ctx.Err() == nil && classifyKubernetesError(err).retryableRead
	}, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return runner.Client.Get(ctx, key, object)
	})
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

func RecordsPass(records []Record, expected int) bool {
	if len(records) != expected {
		return false
	}
	for _, record := range records {
		if !record.Pass {
			return false
		}
	}
	return true
}
