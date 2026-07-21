package webhooktls

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/podbuilder"
)

func TestEnvtestRealTLSAndNarrowAdmissionBypass(t *testing.T) {
	testEnvironment := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			LocalServingHost:             "127.0.0.1",
			LocalServingHostExternalName: "localhost",
		},
	}
	config, err := testEnvironment.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := testEnvironment.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add Kubernetes scheme: %v", err)
	}
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Kontext scheme: %v", err)
	}
	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("create envtest client: %v", err)
	}
	ctx := context.Background()
	if err := k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultNamespace},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create webhook namespace: %v", err)
	}

	now := time.Now
	options := DefaultOptions()
	options.ServiceName = "localhost"
	options.Clock = now
	store := &Store{}
	lifecycle := NewLifecycle(k8sClient, store, options)
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("bootstrap self-managed TLS: %v", err)
	}
	var registration admissionv1.MutatingWebhookConfiguration
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get converged registration: %v", err)
	}
	resourceVersion := registration.ResourceVersion
	if err := lifecycle.Ensure(ctx); err != nil {
		t.Fatalf("repeat self-managed TLS reconciliation: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get registration after repeated reconciliation: %v", err)
	}
	if registration.ResourceVersion != resourceVersion {
		t.Fatalf(
			"unchanged registration was written again: resourceVersion %s became %s",
			resourceVersion,
			registration.ResourceVersion,
		)
	}

	server := webhook.NewServer(webhook.Options{
		Host: testEnvironment.WebhookInstallOptions.LocalServingHost,
		Port: testEnvironment.WebhookInstallOptions.LocalServingPort,
		TLSOpts: []func(*tls.Config){
			TLSOption(store),
		},
	})
	server.Register(DefaultWebhookPath, Handler(k8sClient, scheme))
	serverContext, cancelServer := context.WithCancel(ctx)
	t.Cleanup(cancelServer)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Start(serverContext)
	}()
	waitForWebhookServer(t, server)

	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("get registration: %v", err)
	}
	url := fmt.Sprintf(
		"https://localhost:%d%s",
		testEnvironment.WebhookInstallOptions.LocalServingPort,
		DefaultWebhookPath,
	)
	registration.Webhooks[0].ClientConfig.Service = nil
	registration.Webhooks[0].ClientConfig.URL = &url
	if err := k8sClient.Update(ctx, &registration); err != nil {
		t.Fatalf("point envtest registration at TLS server: %v", err)
	}

	staticAgent := testTaskAgent("static-task", "static goal", "")
	if err := k8sClient.Create(ctx, staticAgent); err != nil {
		t.Fatalf("create static Task Agent: %v", err)
	}
	templateAgent := testTaskAgent(
		"template-task",
		"",
		"Summarize ${area}; keep $${literal}; repeat ${area}.",
	)
	if err := k8sClient.Create(ctx, templateAgent); err != nil {
		t.Fatalf("create parameterized Task Agent: %v", err)
	}
	badTemplateAgent := testTaskAgent("bad-template", "", "broken ${name")
	if err := k8sClient.Create(ctx, badTemplateAgent); err != nil {
		t.Fatalf("create malformed Task Agent: %v", err)
	}
	serviceAgent := testTaskAgent("service-agent", "serve", "")
	serviceAgent.Spec.Mode = kontextv1alpha1.AgentModeService
	if err := k8sClient.Create(ctx, serviceAgent); err != nil {
		t.Fatalf("create Service Agent: %v", err)
	}
	scheduledAgent := testTaskAgent("scheduled-agent", "scheduled", "")
	scheduledAgent.Spec.Mode = kontextv1alpha1.AgentModeScheduled
	scheduledAgent.Spec.Schedule = &kontextv1alpha1.ScheduleSpec{Expression: "0 * * * *"}
	if err := k8sClient.Create(ctx, scheduledAgent); err != nil {
		t.Fatalf("create Scheduled Agent: %v", err)
	}

	complete := testAgentRun("complete-bypass", completeSpec())
	if err := k8sClient.Create(ctx, complete); err != nil {
		t.Fatalf("create nonmatching complete AgentRun: %v", err)
	}

	staticInvocation := testAgentRun("static-through-webhook", map[string]any{
		"agentRef":   map[string]any{"name": staticAgent.Name},
		"parameters": map[string]any{},
	})
	if err := k8sClient.Create(ctx, staticInvocation); err != nil {
		t.Fatalf("create static sparse Task invocation: %v", err)
	}
	var staticRun kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKey{
		Namespace: "default",
		Name:      staticInvocation.GetName(),
	}, &staticRun); err != nil {
		t.Fatalf("get resolved static Task run: %v", err)
	}
	assertResolvedTaskRun(t, &staticRun, staticAgent, "static goal", nil)

	templateInvocation := testAgentRun("template-through-webhook", map[string]any{
		"agentRef":   map[string]any{"name": templateAgent.Name},
		"parameters": map[string]any{"area": "API\nserver"},
	})
	templateInvocation.SetLabels(map[string]string{"example.test/source": "envtest"})
	if err := k8sClient.Create(ctx, templateInvocation); err != nil {
		t.Fatalf("create parameterized sparse Task invocation: %v", err)
	}
	var templateRun kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKey{
		Namespace: "default",
		Name:      templateInvocation.GetName(),
	}, &templateRun); err != nil {
		t.Fatalf("get resolved parameterized Task run: %v", err)
	}
	assertResolvedTaskRun(
		t,
		&templateRun,
		templateAgent,
		"Summarize API\nserver; keep ${literal}; repeat API\nserver.",
		map[string]string{"area": "API\nserver"},
	)
	if templateRun.Labels["example.test/source"] != "envtest" {
		t.Fatalf("invocation label was not retained: %#v", templateRun.Labels)
	}

	rejections := []struct {
		name      string
		agentName string
		spec      map[string]any
		want      string
	}{
		{
			name:      "missing-agent",
			agentName: "does-not-exist",
			want:      "MissingAgent",
		},
		{
			name:      "wrong-mode",
			agentName: serviceAgent.Name,
			want:      "WrongMode",
		},
		{
			name:      "missing-parameter",
			agentName: templateAgent.Name,
			want:      "missing parameters: area",
		},
		{
			name:      "unused-parameter",
			agentName: templateAgent.Name,
			spec:      map[string]any{"parameters": map[string]any{"area": "api", "extra": "no"}},
			want:      "unused parameters: extra",
		},
		{
			name:      "static-parameters",
			agentName: staticAgent.Name,
			spec:      map[string]any{"parameters": map[string]any{"extra": "no"}},
			want:      "unused parameters: extra",
		},
		{
			name:      "malformed-template",
			agentName: badTemplateAgent.Name,
			spec:      map[string]any{"parameters": map[string]any{"name": "value"}},
			want:      "InvalidTemplate",
		},
		{
			name:      "locked-goal",
			agentName: staticAgent.Name,
			spec:      map[string]any{"goal": "override"},
			want:      "locked fields: goal",
		},
	}
	for _, test := range rejections {
		t.Run(test.name, func(t *testing.T) {
			spec := map[string]any{
				"agentRef": map[string]any{"name": test.agentName},
			}
			for key, value := range test.spec {
				spec[key] = value
			}
			err := k8sClient.Create(ctx, testAgentRun("reject-"+test.name, spec))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("create error = %v, want text %q", err, test.want)
			}
		})
	}

	originalGoal := templateRun.Spec.Goal
	templateRun.Spec.Goal = "mutated"
	err = k8sClient.Update(ctx, &templateRun)
	if !apierrors.IsInvalid(err) {
		t.Fatalf("resolved Task spec update should be immutable, got %v", err)
	}
	templateRun.Spec.Goal = originalGoal

	completeService := testAgentRun("complete-service-bypass", map[string]any{
		"agentRef": map[string]any{"name": serviceAgent.Name},
		"goal":     "controller snapshot",
		"provider": "echo",
		"model":    "echo-model",
		"runtime":  map[string]any{"image": "example.invalid/controller:test"},
	})
	if err := k8sClient.Create(ctx, completeService); err != nil {
		t.Fatalf("create complete Service snapshot: %v", err)
	}
	var persistedService unstructured.Unstructured
	persistedService.SetGroupVersionKind(completeService.GroupVersionKind())
	if err := k8sClient.Get(ctx, client.ObjectKey{
		Namespace: "default",
		Name:      completeService.GetName(),
	}, &persistedService); err != nil {
		t.Fatalf("get complete Service snapshot: %v", err)
	}
	if !reflect.DeepEqual(persistedService.Object["spec"], completeService.Object["spec"]) {
		t.Fatalf(
			"complete Service snapshot changed:\ngot:  %#v\nwant: %#v",
			persistedService.Object["spec"],
			completeService.Object["spec"],
		)
	}
	completeScheduled := testAgentRun("complete-scheduled-bypass", map[string]any{
		"agentRef": map[string]any{"name": scheduledAgent.Name},
		"goal":     "scheduled controller snapshot",
		"provider": "echo",
		"model":    "echo-model",
		"runtime":  map[string]any{"image": "example.invalid/controller:test"},
	})
	if err := k8sClient.Create(ctx, completeScheduled); err != nil {
		t.Fatalf("create complete Scheduled snapshot: %v", err)
	}
	var persistedScheduled unstructured.Unstructured
	persistedScheduled.SetGroupVersionKind(completeScheduled.GroupVersionKind())
	if err := k8sClient.Get(ctx, client.ObjectKey{
		Namespace: "default",
		Name:      completeScheduled.GetName(),
	}, &persistedScheduled); err != nil {
		t.Fatalf("get complete Scheduled snapshot: %v", err)
	}
	if !reflect.DeepEqual(persistedScheduled.Object["spec"], completeScheduled.Object["spec"]) {
		t.Fatalf(
			"complete Scheduled snapshot changed:\ngot:  %#v\nwant: %#v",
			persistedScheduled.Object["spec"],
			completeScheduled.Object["spec"],
		)
	}

	if err := k8sClient.Get(ctx, client.ObjectKey{Name: DefaultWebhookName}, &registration); err != nil {
		t.Fatalf("refresh registration: %v", err)
	}
	registration.Webhooks[0].ClientConfig.CABundle = []byte("untrusted")
	if err := k8sClient.Update(ctx, &registration); err != nil {
		t.Fatalf("break admission trust: %v", err)
	}
	untrustedSparse := testAgentRun("sparse-fails-closed", map[string]any{
		"agentRef": map[string]any{"name": staticAgent.Name},
	})
	err = k8sClient.Create(ctx, untrustedSparse)
	if err == nil || apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "failed calling webhook") {
		t.Fatalf("matching request did not fail closed under broken TLS: %v", err)
	}
	unaffected := testAgentRun("complete-still-bypasses", completeSpec())
	if err := k8sClient.Create(ctx, unaffected); err != nil {
		t.Fatalf("nonmatching AgentRun depended on broken webhook TLS: %v", err)
	}

	cancelServer()
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatalf("webhook server stopped with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook server did not stop")
	}
}

func waitForWebhookServer(t *testing.T, server webhook.Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := server.StartedChecker()(nil); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("webhook server did not become ready")
}

func testAgentRun(name string, spec map[string]any) *unstructured.Unstructured {
	run := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kontext.dev/v1alpha1",
		"kind":       "AgentRun",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "default",
		},
		"spec": spec,
	}}
	run.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "kontext.dev", Version: "v1alpha1", Kind: "AgentRun",
	})
	return run
}

func completeSpec() map[string]any {
	return map[string]any{
		"goal":  "complete",
		"model": "test/model",
		"runtime": map[string]any{
			"image": "example.invalid/runtime:test",
		},
	}
}

func testTaskAgent(name string, goal string, goalTemplate string) *kontextv1alpha1.Agent {
	return &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:         kontextv1alpha1.AgentModeTask,
			Goal:         goal,
			GoalTemplate: goalTemplate,
			Provider:     " ECHO ",
			Model:        "echo-model",
			Tools:        []string{"shell"},
			Budget:       &kontextv1alpha1.BudgetSpec{Wallclock: "2m"},
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image: "example.invalid/task:test",
				Args:  []string{"run"},
			},
		},
	}
}

func assertResolvedTaskRun(
	t *testing.T,
	run *kontextv1alpha1.AgentRun,
	agent *kontextv1alpha1.Agent,
	goal string,
	parameters map[string]string,
) {
	t.Helper()
	if run.Spec.AgentRef == nil || run.Spec.AgentRef.Name != agent.Name ||
		run.Spec.Goal != goal ||
		run.Spec.Provider != "echo" ||
		run.Spec.Model != agent.Spec.Model ||
		run.Spec.Runtime.Image != agent.Spec.Runtime.Image ||
		!reflect.DeepEqual(run.Spec.Parameters, parameters) {
		t.Fatalf("persisted Task snapshot is incomplete: %#v", run.Spec)
	}
	if run.Labels[podbuilder.LabelAgentName] != agent.Name {
		t.Fatalf("canonical Agent label = %#v", run.Labels)
	}
	if !metav1.IsControlledBy(run, agent) {
		t.Fatalf("Task run is not controller-owned by Agent: %#v", run.OwnerReferences)
	}
	if !reflect.DeepEqual(run.Status, kontextv1alpha1.AgentRunStatus{}) {
		t.Fatalf("admission unexpectedly created observable status: %#v", run.Status)
	}
}
