package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

type terminalClient struct {
	client.Client
	deletes int
	podGets int
	phase   kontextv1alpha1.AgentRunPhase
	message string
}

func (cluster *terminalClient) Create(
	ctx context.Context,
	object client.Object,
	options ...client.CreateOption,
) error {
	if err := cluster.Client.Create(ctx, object, options...); err != nil {
		return err
	}
	run, ok := object.(*kontextv1alpha1.AgentRun)
	if !ok {
		return nil
	}
	exitCode := int32(0)
	pod := &corev1.Pod{}
	pod.Namespace = run.Namespace
	pod.Name = "pod-" + run.Name
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "runtime",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: exitCode,
		}},
	}}
	return cluster.Client.Create(ctx, pod)
}

func (cluster *terminalClient) Get(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
	options ...client.GetOption,
) error {
	if err := cluster.Client.Get(ctx, key, object, options...); err != nil {
		return err
	}
	if run, ok := object.(*kontextv1alpha1.AgentRun); ok {
		phase := cluster.phase
		if phase == "" {
			phase = kontextv1alpha1.AgentRunPhaseSucceeded
		}
		run.Status.Phase = phase
		run.Status.Message = cluster.message
		if run.Status.Message == "" {
			run.Status.Message = "successful status message"
		}
		run.Status.PodName = "pod-" + run.Name
		run.Status.Result = "sensitive model output"
		value := int64(7)
		run.Status.Usage = &kontextv1alpha1.UsageStatus{Tokens: &value}
		run.Status.Output = &kontextv1alpha1.OutputStatus{
			MediaType: "text/plain",
			Value:     runtime.RawExtension{Raw: []byte(`"sensitive model output"`)},
		}
	}
	if pod, ok := object.(*corev1.Pod); ok {
		cluster.podGets++
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "runtime",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0,
				Message:  successfulEnvelopeMessage(),
			}},
		}}
	}
	return nil
}

func (cluster *terminalClient) Delete(
	ctx context.Context,
	object client.Object,
	options ...client.DeleteOption,
) error {
	cluster.deletes++
	return cluster.Client.Delete(ctx, object, options...)
}

type createFailClient struct {
	client.Client
	deletes int
}

func (cluster *createFailClient) Create(
	context.Context,
	client.Object,
	...client.CreateOption,
) error {
	return apierrors.NewForbidden(
		schema.GroupResource{Group: "kontext.dev", Resource: "agentruns"},
		"case",
		errors.New("forbidden"),
	)
}

func (cluster *createFailClient) Delete(
	context.Context,
	client.Object,
	...client.DeleteOption,
) error {
	cluster.deletes++
	return nil
}

type staticLogs struct {
	data      []byte
	err       error
	truncated bool
	calls     int
}

func (logs *staticLogs) Fetch(context.Context, string, string, string) (LogCollection, error) {
	logs.calls++
	return LogCollection{
		Data:      append([]byte(nil), logs.data...),
		Truncated: logs.truncated,
	}, logs.err
}

type contextInspectingLogs struct {
	contextErr  error
	hadDeadline bool
}

func (logs *contextInspectingLogs) Fetch(
	ctx context.Context,
	_, _, _ string,
) (LogCollection, error) {
	logs.contextErr = ctx.Err()
	_, logs.hadDeadline = ctx.Deadline()
	return LogCollection{}, nil
}

type orderingJudge struct {
	t      *testing.T
	called bool
}

func (judge *orderingJudge) Evaluate(
	_ context.Context,
	observation JudgeObservation,
) (JudgeResult, error) {
	judge.called = true
	if len(observation.Grades) == 0 {
		judge.t.Fatal("judge ran before deterministic graders")
	}
	return JudgeResult{Configured: true, Pass: true, Score: 1, Rationale: "ok"}, nil
}

type contextInspectingJudge struct {
	contextErr  error
	hadDeadline bool
}

func (judge *contextInspectingJudge) Evaluate(
	ctx context.Context,
	_ JudgeObservation,
) (JudgeResult, error) {
	judge.contextErr = ctx.Err()
	_, judge.hadDeadline = ctx.Deadline()
	return JudgeResult{Configured: true, Pass: true, Score: 1, Rationale: "ok"}, nil
}

func successfulLogs(t *testing.T) []byte {
	t.Helper()
	var logs bytes.Buffer
	logs.WriteString(
		`{"apiVersion":"kontext.dev/event/v1alpha1","timestamp":"2026-07-19T00:00:00Z","type":"lifecycle","data":{"phase":"started"}}` + "\n",
	)
	return logs.Bytes()
}

func successfulEnvelopeMessage() string {
	output, _ := json.Marshal("private output")
	extension, _ := json.Marshal("secret-extension")
	turns := int32(2)
	tools := int32(1)
	envelope := resultv1alpha1.Envelope{
		APIVersion: resultv1alpha1.APIVersion,
		Outcome:    resultv1alpha1.OutcomeSucceeded,
		Output: &resultv1alpha1.Output{
			MediaType: "text/plain",
			Value:     output,
		},
		Execution: &resultv1alpha1.Execution{
			Provider:  "private-provider",
			Model:     "model",
			RequestID: "request-secret",
			Turns:     &turns,
			ToolCalls: &tools,
		},
		Artifacts: []resultv1alpha1.Artifact{{
			Name: "artifact-secret", URI: "memory://artifact",
		}},
		Extensions: map[string]json.RawMessage{
			"example.com/private": extension,
		},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func int32Pointer(value int32) *int32 {
	return &value
}

func mustArtifactRequirements(t *testing.T, graders []Grader) artifactRequirements {
	t.Helper()
	requirements, err := requirementsForGraders(graders)
	if err != nil {
		t.Fatal(err)
	}
	return requirements
}

type cleanupContextClient struct {
	client.Client
	deletes          int
	deleteContextErr error
	hadDeadline      bool
}

func (cluster *cleanupContextClient) Delete(
	ctx context.Context,
	object client.Object,
	options ...client.DeleteOption,
) error {
	cluster.deletes++
	cluster.deleteContextErr = ctx.Err()
	_, cluster.hadDeadline = ctx.Deadline()
	return cluster.Client.Delete(ctx, object, options...)
}

type sequencedGetClient struct {
	client.Client
	agentErrors []error
	podErrors   []error
	agentGets   int
	podGets     int
}

func (cluster *sequencedGetClient) Get(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
	options ...client.GetOption,
) error {
	switch object.(type) {
	case *kontextv1alpha1.AgentRun:
		cluster.agentGets++
		if len(cluster.agentErrors) > 0 {
			err := cluster.agentErrors[0]
			cluster.agentErrors = cluster.agentErrors[1:]
			return err
		}
	case *corev1.Pod:
		cluster.podGets++
		if len(cluster.podErrors) > 0 {
			err := cluster.podErrors[0]
			cluster.podErrors = cluster.podErrors[1:]
			return err
		}
	}
	return cluster.Client.Get(ctx, key, object, options...)
}

func newFakeEvalClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

type blockingCreateClient struct {
	client.Client
}

func (cluster *blockingCreateClient) Create(
	ctx context.Context,
	_ client.Object,
	_ ...client.CreateOption,
) error {
	<-ctx.Done()
	return ctx.Err()
}

type blockingPodGetClient struct {
	client.Client
}

func (cluster *blockingPodGetClient) Get(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
	options ...client.GetOption,
) error {
	if _, ok := object.(*corev1.Pod); ok {
		<-ctx.Done()
		return ctx.Err()
	}
	return cluster.Client.Get(ctx, key, object, options...)
}

type blockingLogs struct{}

func (blockingLogs) Fetch(
	ctx context.Context,
	_, _, _ string,
) (LogCollection, error) {
	<-ctx.Done()
	return LogCollection{}, ctx.Err()
}

type blockingJudge struct{}

func (blockingJudge) Evaluate(
	ctx context.Context,
	_ JudgeObservation,
) (JudgeResult, error) {
	<-ctx.Done()
	return JudgeResult{}, ctx.Err()
}

type ambiguousCreateClient struct {
	client.Client
	unowned bool
	gets    int
	deletes int
}

func (cluster *ambiguousCreateClient) Create(
	ctx context.Context,
	object client.Object,
	_ ...client.CreateOption,
) error {
	run := object.(*kontextv1alpha1.AgentRun).DeepCopy()
	if cluster.unowned {
		run.Labels = map[string]string{"owner": "user"}
	}
	if err := cluster.Client.Create(ctx, run); err != nil {
		return err
	}
	return apierrors.NewTimeoutError("create response lost", 0)
}

func (cluster *ambiguousCreateClient) Get(
	ctx context.Context,
	key types.NamespacedName,
	object client.Object,
	options ...client.GetOption,
) error {
	cluster.gets++
	return cluster.Client.Get(ctx, key, object, options...)
}

func (cluster *ambiguousCreateClient) Delete(
	ctx context.Context,
	object client.Object,
	options ...client.DeleteOption,
) error {
	cluster.deletes++
	return cluster.Client.Delete(ctx, object, options...)
}
