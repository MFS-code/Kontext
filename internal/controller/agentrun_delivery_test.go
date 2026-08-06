package controller_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	deliveryv1alpha1 "github.com/MFS-code/Kontext/pkg/delivery/v1alpha1"
)

func TestAgentRunReconcilerDeliversToStandingService(t *testing.T) {
	var received deliveryv1alpha1.Request
	var receivedHost string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != deliveryv1alpha1.EndpointPath {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Content-Type") != "application/json" ||
			request.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected delivery headers: %#v", request.Header)
		}
		receivedHost = request.Host
		var err error
		received, err = deliveryv1alpha1.Parse(readRequestBody(t, request))
		if err != nil {
			t.Errorf("parse delivery request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","output":{"mediaType":"application/json","value":{"answer":"warm"}},"usage":{"totalTokens":7}}`)
	}))
	t.Cleanup(server.Close)
	host, port := serverAddress(t, server)

	ctx := context.Background()
	fixture := createDeliveryFixture(t, ctx, "delivery-success", host, port)
	reconciler := newAgentRunReconciler()
	result, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Namespace: fixture.delivered.Namespace,
			Name:      fixture.delivered.Name,
		},
	})
	if err != nil {
		t.Fatalf("reconcile delivered run: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("terminal delivery requeued: %#v", result)
	}

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.delivered), &updated); err != nil {
		t.Fatalf("get delivered run: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseSucceeded ||
		updated.Status.PodName != fixture.pod.Name ||
		updated.Status.Result != `{"answer":"warm"}` ||
		updated.Status.Output == nil ||
		string(updated.Status.Output.Value.Raw) != `{"answer":"warm"}` ||
		updated.Status.Usage == nil ||
		updated.Status.Usage.Tokens == nil ||
		*updated.Status.Usage.Tokens != 7 ||
		updated.Status.StartTime == nil ||
		updated.Status.CompletionTime == nil {
		t.Fatalf("unexpected delivered status: %#v", updated.Status)
	}
	if received.APIVersion != deliveryv1alpha1.APIVersion ||
		received.Run.Name != updated.Name ||
		received.Run.Namespace != updated.Namespace ||
		received.Run.UID != string(updated.UID) ||
		received.Target.Name != fixture.pod.Name ||
		received.Target.UID != string(fixture.pod.UID) ||
		receivedHost != fixture.pod.Name ||
		received.Goal != updated.Spec.Goal {
		t.Fatalf("unexpected delivery request: %#v", received)
	}

	var spawned corev1.Pod
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: updated.Namespace,
		Name:      podbuilder.PodNameForRun(updated.Name),
	}, &spawned)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("delivered run spawned a Pod: pod=%#v error=%v", spawned, err)
	}
}

func TestAgentRunReconcilerUsesStandingSnapshotAfterAgentPortChange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded"}`)
	}))
	t.Cleanup(server.Close)
	host, port := serverAddress(t, server)

	ctx := context.Background()
	fixture := createDeliveryFixture(t, ctx, "delivery-agent-drift", host, port)
	var updatedAgent kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.agent), &updatedAgent); err != nil {
		t.Fatalf("get Service Agent: %v", err)
	}
	changedPort := port + 1
	if changedPort > 65535 {
		changedPort = port - 1
	}
	updatedAgent.Spec.Runtime.Delivery.Port = changedPort
	if err := k8sClient.Update(ctx, &updatedAgent); err != nil {
		t.Fatalf("change Service Agent delivery port: %v", err)
	}

	if _, err := newAgentRunReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(fixture.delivered),
	}); err != nil {
		t.Fatalf("reconcile snapshotted delivery: %v", err)
	}
	var completed kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.delivered), &completed); err != nil {
		t.Fatalf("get snapshotted delivery: %v", err)
	}
	if completed.Status.Phase != kontextv1alpha1.AgentRunPhaseSucceeded ||
		completed.Status.PodName != fixture.pod.Name {
		t.Fatalf("mutable Agent port overrode standing snapshot: %#v", completed.Status)
	}
}

func TestAgentRunReconcilerRejectsUnownedDeliverySnapshot(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded"}`)
	}))
	t.Cleanup(server.Close)
	host, port := serverAddress(t, server)

	ctx := context.Background()
	fixture := createDeliveryFixture(t, ctx, "delivery-unowned", host, port)
	forged := deliveredRunForAgent(t, fixture.agent, "delivery-unowned-forged", port)
	forged.OwnerReferences = nil
	if err := k8sClient.Create(ctx, forged); err != nil {
		t.Fatalf("create unowned delivery snapshot: %v", err)
	}

	if _, err := newAgentRunReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(forged),
	}); err != nil {
		t.Fatalf("reconcile unowned delivery: %v", err)
	}
	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(forged), &updated); err != nil {
		t.Fatalf("get rejected delivery: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseFailed ||
		!strings.Contains(updated.Status.Message, "owning Service Agent") ||
		requests.Load() != 0 {
		t.Fatalf("unowned snapshot reached Service runtime: status=%#v requests=%d", updated.Status, requests.Load())
	}
}

func TestAgentRunReconcilerRejectsInvalidDeliveryResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantMessage string
	}{
		{
			name:        "non-200",
			status:      http.StatusServiceUnavailable,
			contentType: "application/json",
			body:        `{"error":"busy"}`,
			wantMessage: "HTTP 503",
		},
		{
			name:        "wrong content type",
			status:      http.StatusOK,
			contentType: "text/plain",
			body:        `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded"}`,
			wantMessage: "Content-Type",
		},
		{
			name:        "legacy result",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"result":"legacy"}`,
			wantMessage: "versioned result envelope required",
		},
		{
			name:        "oversized result",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        strings.Repeat("x", deliveryv1alpha1.MaxResponseBytes+1),
			wantMessage: "exceeded 4096 bytes",
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			}))
			t.Cleanup(server.Close)
			host, port := serverAddress(t, server)
			ctx := context.Background()
			fixture := createDeliveryFixture(
				t,
				ctx,
				fmt.Sprintf("delivery-invalid-%d", index),
				host,
				port,
			)

			if _, err := newAgentRunReconciler().Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKeyFromObject(fixture.delivered),
			}); err != nil {
				t.Fatalf("reconcile invalid response: %v", err)
			}
			var updated kontextv1alpha1.AgentRun
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.delivered), &updated); err != nil {
				t.Fatalf("get failed delivery: %v", err)
			}
			if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseFailed ||
				!strings.Contains(updated.Status.Message, test.wantMessage) {
				t.Fatalf("invalid response status = %#v", updated.Status)
			}
		})
	}
}

func TestAgentRunReconcilerRetriesAgainstRecastService(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseFirstResponse := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			close(requestStarted)
			<-releaseFirstResponse
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","output":{"mediaType":"text/plain","value":"stale result"}}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","output":{"mediaType":"text/plain","value":"recast complete"}}`)
	}))
	t.Cleanup(server.Close)
	host, port := serverAddress(t, server)

	ctx := context.Background()
	fixture := createDeliveryFixture(t, ctx, "delivery-recast", host, port)
	reconciler := newAgentRunReconciler()
	type reconcileResult struct {
		result ctrl.Result
		err    error
	}
	done := make(chan reconcileResult, 1)
	go func() {
		result, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKeyFromObject(fixture.delivered),
		})
		done <- reconcileResult{result: result, err: err}
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first delivery did not reach fake runtime")
	}

	replacementRun, replacementPod := createStandingTarget(
		t,
		ctx,
		fixture.agent,
		"delivery-recast-standing-2",
		host,
	)
	var currentAgent kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.agent), &currentAgent); err != nil {
		t.Fatalf("get Service Agent for recast: %v", err)
	}
	currentAgent.Status.CurrentRunName = replacementRun.Name
	if err := k8sClient.Status().Update(ctx, &currentAgent); err != nil {
		t.Fatalf("publish replacement standing run: %v", err)
	}
	close(releaseFirstResponse)

	first := <-done
	if first.err != nil {
		t.Fatalf("reconcile interrupted delivery: %v", first.err)
	}
	if first.result.RequeueAfter <= 0 {
		t.Fatalf("interrupted delivery did not requeue: %#v", first.result)
	}
	var pending kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.delivered), &pending); err != nil {
		t.Fatalf("get interrupted delivery: %v", err)
	}
	if pending.Status.Phase != kontextv1alpha1.AgentRunPhasePending ||
		!conditionHasReason(pending.Status.Conditions, "TargetChanged") {
		t.Fatalf("interrupted delivery status = %#v", pending.Status)
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(fixture.delivered),
	}); err != nil {
		t.Fatalf("reconcile delivery after recast: %v", err)
	}
	var completed kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.delivered), &completed); err != nil {
		t.Fatalf("get completed recast delivery: %v", err)
	}
	if completed.Status.Phase != kontextv1alpha1.AgentRunPhaseSucceeded ||
		completed.Status.PodName != replacementPod.Name ||
		completed.Status.Result != "recast complete" ||
		requests.Load() != 2 {
		t.Fatalf("delivery did not converge after recast: status=%#v requests=%d", completed.Status, requests.Load())
	}
}

func TestAgentRunReconcilerDoesNotOverwriteTerminalStatusAfterResponse(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-releaseResponse
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack stale delivery response: %v", err)
			return
		}
		_ = connection.Close()
	}))
	t.Cleanup(server.Close)
	host, port := serverAddress(t, server)

	ctx := context.Background()
	fixture := createDeliveryFixture(t, ctx, "delivery-status-race", host, port)
	reconciler := newAgentRunReconciler()
	done := make(chan error, 1)
	go func() {
		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKeyFromObject(fixture.delivered),
		})
		done <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not reach fake runtime")
	}
	var terminal kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.delivered), &terminal); err != nil {
		t.Fatalf("get in-flight delivery: %v", err)
	}
	terminal.Status.Phase = kontextv1alpha1.AgentRunPhaseFailed
	terminal.Status.Message = "A concurrent observer made the run terminal."
	completedAt := metav1.Now()
	terminal.Status.CompletionTime = &completedAt
	if err := k8sClient.Status().Update(ctx, &terminal); err != nil {
		t.Fatalf("publish concurrent terminal status: %v", err)
	}
	close(releaseResponse)
	if err := <-done; err != nil {
		t.Fatalf("finish delivery reconciliation: %v", err)
	}

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.delivered), &updated); err != nil {
		t.Fatalf("get terminal delivery: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseFailed ||
		updated.Status.Message != terminal.Status.Message {
		t.Fatalf("delivery response overwrote terminal status: %#v", updated.Status)
	}
}

func TestAgentRunReconcilerDoesNotApplyResponseToRecreatedRun(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-releaseResponse
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded"}`)
	}))
	t.Cleanup(server.Close)
	host, port := serverAddress(t, server)

	ctx := context.Background()
	fixture := createDeliveryFixture(t, ctx, "delivery-uid-race", host, port)
	originalUID := fixture.delivered.UID
	reconciler := newAgentRunReconciler()
	done := make(chan error, 1)
	go func() {
		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKeyFromObject(fixture.delivered),
		})
		done <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not reach fake runtime")
	}
	if err := k8sClient.Delete(ctx, fixture.delivered); err != nil {
		t.Fatalf("delete in-flight delivery: %v", err)
	}
	runKey := client.ObjectKeyFromObject(fixture.delivered)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var deleted kontextv1alpha1.AgentRun
		err := k8sClient.Get(ctx, runKey, &deleted)
		if apierrors.IsNotFound(err) {
			break
		}
		if err != nil {
			t.Fatalf("observe in-flight deletion: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("in-flight delivery was not deleted")
		}
		time.Sleep(20 * time.Millisecond)
	}

	replacement := deliveredRunForAgent(t, fixture.agent, fixture.delivered.Name, port)
	if err := k8sClient.Create(ctx, replacement); err != nil {
		t.Fatalf("recreate delivered run: %v", err)
	}
	if replacement.UID == originalUID {
		t.Fatalf("recreated run reused UID %q", replacement.UID)
	}
	close(releaseResponse)
	if err := <-done; err != nil {
		t.Fatalf("finish stale delivery reconciliation: %v", err)
	}

	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), &updated); err != nil {
		t.Fatalf("get recreated delivery: %v", err)
	}
	if updated.Status.Phase != "" ||
		updated.Status.Output != nil ||
		updated.Status.CompletionTime != nil {
		t.Fatalf("stale response completed recreated run: %#v", updated.Status)
	}
}

func TestAgentRunReconcilerBoundsMissingServiceWait(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "delivery-timeout-agent", Namespace: "default"},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:         kontextv1alpha1.AgentModeService,
			Goal:         "serve",
			GoalTemplate: "${payload}",
			Model:        "test/model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:    "runtime:test",
				Delivery: &kontextv1alpha1.RuntimeDeliverySpec{Port: 8080},
			},
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create Service Agent: %v", err)
	}
	run := deliveredRunForAgent(t, agent, "delivery-timeout-run", 8080)
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create delivered run: %v", err)
	}

	clock := &fakeClock{now: run.CreationTimestamp.Add(6 * time.Minute)}
	reconciler := newAgentRunReconciler()
	reconciler.Clock = clock
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(run),
	}); err != nil {
		t.Fatalf("reconcile timed-out delivery: %v", err)
	}
	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(run), &updated); err != nil {
		t.Fatalf("get timed-out delivery: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseFailed ||
		!strings.Contains(updated.Status.Message, "timed out after 5m0s") {
		t.Fatalf("missing Service wait was not bounded: %#v", updated.Status)
	}
}

func TestAgentRunReconcilerWaitsWithoutSpawningForMissingTarget(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "delivery-wait-agent", Namespace: "default"},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:         kontextv1alpha1.AgentModeService,
			Goal:         "serve",
			GoalTemplate: "${payload}",
			Model:        "test/model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:    "runtime:test",
				Delivery: &kontextv1alpha1.RuntimeDeliverySpec{Port: 8080},
			},
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create Service Agent: %v", err)
	}
	run := deliveredRunForAgent(t, agent, "delivery-wait-run", 8080)
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create delivered run: %v", err)
	}

	result, err := newAgentRunReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(run),
	})
	if err != nil {
		t.Fatalf("reconcile waiting delivery: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("waiting delivery did not requeue: %#v", result)
	}
	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(run), &updated); err != nil {
		t.Fatalf("get waiting delivery: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhasePending ||
		updated.Status.StartTime != nil ||
		!conditionHasReason(updated.Status.Conditions, "WaitingForService") {
		t.Fatalf("unexpected waiting status: %#v", updated.Status)
	}
	var spawned corev1.Pod
	err = k8sClient.Get(ctx, types.NamespacedName{
		Namespace: run.Namespace,
		Name:      podbuilder.PodNameForRun(run.Name),
	}, &spawned)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("waiting delivery spawned a Pod: pod=%#v error=%v", spawned, err)
	}
}

func TestAgentRunReconcilerEnforcesDeliveryWallclockBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		<-request.Context().Done()
		time.Sleep(10 * time.Millisecond)
	}))
	t.Cleanup(server.Close)
	host, port := serverAddress(t, server)

	ctx := context.Background()
	fixture := createDeliveryFixture(t, ctx, "delivery-budget", host, port)
	budgeted := deliveredRunForAgent(t, fixture.agent, "delivery-budget-limited-run", port)
	budgeted.Spec.Budget = &kontextv1alpha1.BudgetSpec{Wallclock: "20ms"}
	if err := k8sClient.Create(ctx, budgeted); err != nil {
		t.Fatalf("create budgeted delivered run: %v", err)
	}

	if _, err := newAgentRunReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(budgeted),
	}); err != nil {
		t.Fatalf("reconcile budgeted delivery: %v", err)
	}
	var updated kontextv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(budgeted), &updated); err != nil {
		t.Fatalf("get budget-exceeded delivery: %v", err)
	}
	if updated.Status.Phase != kontextv1alpha1.AgentRunPhaseBudgetExceeded ||
		!strings.Contains(updated.Status.Message, "20ms") {
		t.Fatalf("delivery wallclock budget was not enforced: %#v", updated.Status)
	}
	if _, err := newAgentRunReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(budgeted),
	}); err != nil {
		t.Fatalf("reconcile terminal budget-exceeded delivery: %v", err)
	}
	var standingPod corev1.Pod
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fixture.pod), &standingPod); err != nil {
		t.Fatalf("budget enforcement deleted standing Service Pod: %v", err)
	}
}

func TestServiceRecastAccountingIgnoresDeliveredRunNames(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "delivery-accounting", Namespace: "default"},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:         kontextv1alpha1.AgentModeService,
			Goal:         "serve",
			GoalTemplate: "${payload}",
			Model:        "test/model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:    "runtime:test",
				Delivery: &kontextv1alpha1.RuntimeDeliverySpec{Port: 8080},
			},
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create Service Agent: %v", err)
	}
	standing, _ := createStandingTarget(t, ctx, agent, agent.Name+"-1", "127.0.0.1")
	delivered := deliveredRunForAgent(t, agent, agent.Name+"-999", 8080)
	if err := k8sClient.Create(ctx, delivered); err != nil {
		t.Fatalf("create suffix-shaped delivered run: %v", err)
	}

	reconcileAgent(ctx, t, client.ObjectKeyFromObject(agent))
	var updated kontextv1alpha1.Agent
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(agent), &updated); err != nil {
		t.Fatalf("get reconciled Service Agent: %v", err)
	}
	if updated.Status.CurrentRunName != standing.Name ||
		updated.Status.RunsCreated != 1 ||
		updated.Status.Restarts != 0 {
		t.Fatalf("delivered run affected Service recast accounting: %#v", updated.Status)
	}
}

func TestServiceRecastRejectsDeliveredRunNameCollision(t *testing.T) {
	ctx := context.Background()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "delivery-collision", Namespace: "default"},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:         kontextv1alpha1.AgentModeService,
			Goal:         "serve",
			GoalTemplate: "${payload}",
			Model:        "test/model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:    "runtime:test",
				Delivery: &kontextv1alpha1.RuntimeDeliverySpec{Port: 8080},
			},
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create Service Agent: %v", err)
	}
	delivered := deliveredRunForAgent(t, agent, agent.Name+"-1", 8080)
	if err := k8sClient.Create(ctx, delivered); err != nil {
		t.Fatalf("create colliding delivered run: %v", err)
	}

	_, err := newAgentReconciler().Reconcile(ctx, ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(agent),
	})
	if err == nil || !strings.Contains(err.Error(), "service run name collision") {
		t.Fatalf("recast collision error = %v", err)
	}
}

type deliveryFixture struct {
	agent     *kontextv1alpha1.Agent
	standing  *kontextv1alpha1.AgentRun
	pod       *corev1.Pod
	delivered *kontextv1alpha1.AgentRun
}

func createDeliveryFixture(
	t *testing.T,
	ctx context.Context,
	prefix string,
	podIP string,
	port int32,
) deliveryFixture {
	t.Helper()
	agent := &kontextv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: prefix + "-agent", Namespace: "default"},
		Spec: kontextv1alpha1.AgentSpec{
			Mode:         kontextv1alpha1.AgentModeService,
			Goal:         "serve",
			GoalTemplate: "Handle ${payload}.",
			Provider:     "fake",
			Model:        "test/model",
			Runtime: kontextv1alpha1.RuntimeSpec{
				Image:    "runtime:test",
				Delivery: &kontextv1alpha1.RuntimeDeliverySpec{Port: port},
			},
		},
	}
	if err := k8sClient.Create(ctx, agent); err != nil {
		t.Fatalf("create Service Agent: %v", err)
	}
	standing, pod := createStandingTarget(t, ctx, agent, prefix+"-standing-1", podIP)
	agent.Status.CurrentRunName = standing.Name
	if err := k8sClient.Status().Update(ctx, agent); err != nil {
		t.Fatalf("publish current standing run: %v", err)
	}
	delivered := deliveredRunForAgent(t, agent, prefix+"-run", port)
	if err := k8sClient.Create(ctx, delivered); err != nil {
		t.Fatalf("create delivered run: %v", err)
	}
	return deliveryFixture{
		agent:     agent,
		standing:  standing,
		pod:       pod,
		delivered: delivered,
	}
}

func createStandingTarget(
	t *testing.T,
	ctx context.Context,
	agent *kontextv1alpha1.Agent,
	runName string,
	podIP string,
) (*kontextv1alpha1.AgentRun, *corev1.Pod) {
	t.Helper()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: agent.Namespace},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef: &kontextv1alpha1.AgentRef{Name: agent.Name},
			Goal:     agent.Spec.Goal,
			Provider: agent.Spec.Provider,
			Model:    agent.Spec.Model,
			Runtime:  agent.Spec.Runtime,
		},
	}
	if err := controllerutil.SetControllerReference(agent, run, scheme); err != nil {
		t.Fatalf("own standing run: %v", err)
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatalf("create standing run: %v", err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName + "-pod",
			Namespace: run.Namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  podbuilder.RuntimeContainerName,
				Image: "runtime:test",
			}},
		},
	}
	if err := controllerutil.SetControllerReference(run, pod, scheme); err != nil {
		t.Fatalf("own standing Pod: %v", err)
	}
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Fatalf("create standing Pod: %v", err)
	}
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodRunning,
		PodIP: podIP,
		Conditions: []corev1.PodCondition{{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  podbuilder.RuntimeContainerName,
			Ready: true,
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()},
			},
		}},
	}
	if err := k8sClient.Status().Update(ctx, pod); err != nil {
		t.Fatalf("mark standing Pod ready: %v", err)
	}
	run.Status = kontextv1alpha1.AgentRunStatus{
		Phase:   kontextv1alpha1.AgentRunPhaseRunning,
		PodName: pod.Name,
	}
	if err := k8sClient.Status().Update(ctx, run); err != nil {
		t.Fatalf("mark standing run active: %v", err)
	}
	return run, pod
}

func deliveredRunForAgent(
	t *testing.T,
	agent *kontextv1alpha1.Agent,
	name string,
	port int32,
) *kontextv1alpha1.AgentRun {
	t.Helper()
	run := &kontextv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: agent.Namespace},
		Spec: kontextv1alpha1.AgentRunSpec{
			AgentRef:   &kontextv1alpha1.AgentRef{Name: agent.Name},
			Parameters: map[string]string{"payload": "request"},
			Delivery:   &kontextv1alpha1.AgentRunDeliverySpec{Port: port},
			Goal:       "Handle request.",
			Provider:   agent.Spec.Provider,
			Model:      agent.Spec.Model,
			Runtime:    agent.Spec.Runtime,
		},
	}
	if err := controllerutil.SetControllerReference(agent, run, scheme); err != nil {
		t.Fatalf("own delivered run: %v", err)
	}
	return run
}

func serverAddress(t *testing.T, server *httptest.Server) (string, int32) {
	t.Helper()
	host, rawPort, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("parse fake runtime address: %v", err)
	}
	port, err := strconv.ParseInt(rawPort, 10, 32)
	if err != nil {
		t.Fatalf("parse fake runtime port: %v", err)
	}
	return host, int32(port)
}

func readRequestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	defer request.Body.Close()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read delivery request: %v", err)
	}
	return body
}

func conditionHasReason(conditionsList []metav1.Condition, reason string) bool {
	for _, condition := range conditionsList {
		if condition.Type == conditions.Progressing && condition.Reason == reason {
			return true
		}
	}
	return false
}
