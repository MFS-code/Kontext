package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/conditions"
	"github.com/MFS-code/Kontext/internal/podbuilder"
	"github.com/MFS-code/Kontext/internal/status"
	deliveryv1alpha1 "github.com/MFS-code/Kontext/pkg/delivery/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

const (
	defaultDeliveryTimeout  = 5 * time.Minute
	maxConcurrentAgentRuns  = 8
	initialDeliveryBackoff  = time.Second
	maxDeliveryBackoff      = 15 * time.Second
	deliveryContentTypeJSON = "application/json"
)

var defaultDeliveryClient = newDefaultDeliveryClient()

// HTTPDoer is the outbound HTTP boundary used for warm delivery.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type resolvedDeliveryTarget struct {
	pod *corev1.Pod
}

type deliveryUnavailable struct {
	reason  string
	message string
}

type deliveryConfigurationError struct {
	message string
}

func (err *deliveryConfigurationError) Error() string {
	return err.message
}

func (r *AgentRunReconciler) reconcileDelivery(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
) (ctrl.Result, error) {
	if run.Status.Phase.IsTerminal() {
		return ctrl.Result{}, nil
	}

	wallclockLimit, err := parseWallclockBudget(run.Spec.Budget)
	if err != nil {
		return ctrl.Result{}, r.transitionRun(
			ctx,
			run,
			kontextv1alpha1.AgentRunPhaseFailed,
			fmt.Sprintf("Agent run delivery configuration is invalid: %v.", err),
			nil,
		)
	}

	now := r.now()
	requestDeadline, budgetDeadline := r.deliveryDeadline(run, wallclockLimit, now)
	if !now.Before(requestDeadline) {
		if budgetDeadline {
			return ctrl.Result{}, r.exceedDeliveryBudget(ctx, run, *wallclockLimit)
		}
		return ctrl.Result{}, r.failDeliveryTimeout(ctx, run)
	}

	target, unavailable, err := r.resolveDeliveryTarget(ctx, run)
	if err != nil {
		var configurationErr *deliveryConfigurationError
		if errors.As(err, &configurationErr) {
			return ctrl.Result{}, r.transitionRun(
				ctx,
				run,
				kontextv1alpha1.AgentRunPhaseFailed,
				configurationErr.Error(),
				nil,
			)
		}
		return ctrl.Result{}, err
	}
	if unavailable != nil {
		if err := r.setDeliveryWaiting(ctx, run, unavailable.reason, unavailable.message); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.deliveryBackoff(run)}, nil
	}

	if err := r.setDeliveryRunning(ctx, run, target.pod.Name); err != nil {
		return ctrl.Result{}, err
	}
	confirmedTarget, unavailable, err := r.confirmDeliveryTarget(ctx, run, target)
	if err != nil {
		var configurationErr *deliveryConfigurationError
		if errors.As(err, &configurationErr) {
			return ctrl.Result{}, r.transitionRun(
				ctx,
				run,
				kontextv1alpha1.AgentRunPhaseFailed,
				configurationErr.Error(),
				nil,
			)
		}
		return ctrl.Result{}, err
	}
	if unavailable != nil {
		if err := r.setDeliveryWaiting(ctx, run, unavailable.reason, unavailable.message); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.deliveryBackoff(run)}, nil
	}
	target = confirmedTarget

	now = r.now()
	requestDeadline, budgetDeadline = r.deliveryDeadline(run, wallclockLimit, now)
	if !now.Before(requestDeadline) {
		if budgetDeadline {
			return ctrl.Result{}, r.exceedDeliveryBudget(ctx, run, *wallclockLimit)
		}
		return ctrl.Result{}, r.failDeliveryTimeout(ctx, run)
	}

	originalUID := run.UID
	runKey := client.ObjectKeyFromObject(run)
	envelope, retry, deliveryErr := r.deliver(ctx, run, target.pod, requestDeadline)

	var latest kontextv1alpha1.AgentRun
	if err := r.APIReader.Get(ctx, runKey, &latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if latest.UID != originalUID || latest.Status.Phase.IsTerminal() {
		return ctrl.Result{}, nil
	}
	*run = latest
	confirmedTarget, unavailable, err = r.confirmDeliveryTarget(ctx, run, target)
	if err != nil {
		var configurationErr *deliveryConfigurationError
		if errors.As(err, &configurationErr) {
			return ctrl.Result{}, r.transitionRun(
				ctx,
				run,
				kontextv1alpha1.AgentRunPhaseFailed,
				configurationErr.Error(),
				nil,
			)
		}
		return ctrl.Result{}, err
	}
	if unavailable != nil {
		if err := r.setDeliveryWaiting(ctx, run, unavailable.reason, unavailable.message); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.deliveryBackoff(run)}, nil
	}
	target = confirmedTarget

	if deliveryErr != nil {
		if !retry {
			return ctrl.Result{}, r.transitionRun(
				ctx,
				run,
				kontextv1alpha1.AgentRunPhaseFailed,
				deliveryErr.Error(),
				nil,
			)
		}
		now = r.now()
		requestDeadline, budgetDeadline = r.deliveryDeadline(run, wallclockLimit, now)
		if !now.Before(requestDeadline) {
			if budgetDeadline {
				return ctrl.Result{}, r.exceedDeliveryBudget(ctx, run, *wallclockLimit)
			}
			return ctrl.Result{}, r.failDeliveryTimeout(ctx, run)
		}
		message := fmt.Sprintf("Service delivery target is unreachable: %v.", deliveryErr)
		if patchErr := r.setDeliveryWaiting(ctx, run, "TargetUnreachable", message); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{RequeueAfter: r.deliveryBackoff(run)}, nil
	}

	observation := status.ObserveResultEnvelope(envelope)
	return ctrl.Result{}, r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = observation.Phase
		next.Message = observation.Message
		next.Result = observation.Result
		next.Output = observation.Output
		next.Usage = observation.Usage
		next.PodName = target.pod.Name
		if next.CompletionTime == nil {
			next.CompletionTime = r.nowPtr()
		}
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			conditions.ForAgentRunPhase(observation.Phase)...,
		)
	})
}

func (r *AgentRunReconciler) resolveDeliveryTarget(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
) (*resolvedDeliveryTarget, *deliveryUnavailable, error) {
	if r.APIReader == nil {
		return nil, nil, fmt.Errorf(
			"cannot resolve delivery target for AgentRun %s/%s: APIReader is not configured",
			run.Namespace,
			run.Name,
		)
	}
	if run.Spec.AgentRef == nil || run.Spec.AgentRef.Name == "" {
		return nil, nil, &deliveryConfigurationError{
			message: "Agent run delivery configuration is invalid: agentRef is required.",
		}
	}

	var agent kontextv1alpha1.Agent
	agentKey := client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.AgentRef.Name}
	if err := r.APIReader.Get(ctx, agentKey, &agent); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &deliveryUnavailable{
				reason:  "TargetMissing",
				message: fmt.Sprintf("Referenced Service Agent %s is not available.", run.Spec.AgentRef.Name),
			}, nil
		}
		return nil, nil, err
	}
	if agent.Spec.Mode != kontextv1alpha1.AgentModeService ||
		!metav1.IsControlledBy(run, &agent) {
		return nil, nil, &deliveryConfigurationError{
			message: "Agent run delivery target is not the referenced owning Service Agent.",
		}
	}
	if agent.Status.CurrentRunName == "" {
		return nil, &deliveryUnavailable{
			reason:  "WaitingForService",
			message: "Referenced Service Agent does not have a standing run yet.",
		}, nil
	}

	var standingRun kontextv1alpha1.AgentRun
	standingKey := client.ObjectKey{Namespace: run.Namespace, Name: agent.Status.CurrentRunName}
	if err := r.APIReader.Get(ctx, standingKey, &standingRun); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &deliveryUnavailable{
				reason:  "ServiceRecasting",
				message: "Referenced Service Agent is replacing its standing run.",
			}, nil
		}
		return nil, nil, err
	}
	if !metav1.IsControlledBy(&standingRun, &agent) ||
		standingRun.Spec.Delivery != nil ||
		standingRun.Status.Phase != kontextv1alpha1.AgentRunPhaseRunning ||
		standingRun.Status.PodName == "" {
		return nil, &deliveryUnavailable{
			reason:  "TargetNotReady",
			message: "Referenced Service Agent's standing run is not ready for delivery.",
		}, nil
	}
	if standingRun.Spec.Runtime.Delivery == nil ||
		standingRun.Spec.Runtime.Delivery.Port != run.Spec.Delivery.Port {
		return nil, &deliveryUnavailable{
			reason:  "ServiceRecasting",
			message: "Waiting for a standing Service runtime compatible with the delivery snapshot.",
		}, nil
	}

	var pod corev1.Pod
	podKey := client.ObjectKey{Namespace: run.Namespace, Name: standingRun.Status.PodName}
	if err := r.APIReader.Get(ctx, podKey, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &deliveryUnavailable{
				reason:  "ServiceRecasting",
				message: "Referenced Service Agent's standing Pod is being replaced.",
			}, nil
		}
		return nil, nil, err
	}
	if !metav1.IsControlledBy(&pod, &standingRun) || !deliveryPodReady(&pod) {
		return nil, &deliveryUnavailable{
			reason:  "TargetNotReady",
			message: "Referenced Service Agent's standing Pod is not ready for delivery.",
		}, nil
	}
	return &resolvedDeliveryTarget{pod: &pod}, nil, nil
}

func (r *AgentRunReconciler) confirmDeliveryTarget(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	expected *resolvedDeliveryTarget,
) (*resolvedDeliveryTarget, *deliveryUnavailable, error) {
	current, unavailable, err := r.resolveDeliveryTarget(ctx, run)
	if err != nil || unavailable != nil {
		return nil, unavailable, err
	}
	if expected == nil ||
		expected.pod == nil ||
		expected.pod.UID == "" ||
		current.pod.UID != expected.pod.UID ||
		current.pod.Namespace != expected.pod.Namespace ||
		current.pod.Name != expected.pod.Name ||
		current.pod.Status.PodIP != expected.pod.Status.PodIP {
		return nil, &deliveryUnavailable{
			reason:  "TargetChanged",
			message: "Standing Service Pod identity changed during delivery.",
		}, nil
	}
	return current, nil, nil
}

func deliveryPodReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil ||
		pod.Status.Phase != corev1.PodRunning ||
		pod.Status.PodIP == "" {
		return false
	}
	podReady := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			podReady = true
			break
		}
	}
	if !podReady {
		return false
	}
	for _, container := range pod.Status.ContainerStatuses {
		if container.Name == podbuilder.RuntimeContainerName {
			return container.Ready && container.State.Running != nil
		}
	}
	return false
}

func (r *AgentRunReconciler) deliver(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	pod *corev1.Pod,
	deadline time.Time,
) (resultv1alpha1.Envelope, bool, error) {
	payload, err := json.Marshal(deliveryv1alpha1.Request{
		APIVersion: deliveryv1alpha1.APIVersion,
		Run: deliveryv1alpha1.RunIdentity{
			Name:      run.Name,
			Namespace: run.Namespace,
			UID:       string(run.UID),
		},
		Target: deliveryv1alpha1.PodIdentity{
			Name: pod.Name,
			UID:  string(pod.UID),
		},
		Goal: run.Spec.Goal,
	})
	if err != nil {
		return resultv1alpha1.Envelope{}, false, fmt.Errorf("encode Service delivery request: %w", err)
	}

	requestContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	endpoint := url.URL{
		Scheme: "http",
		Host: net.JoinHostPort(
			pod.Status.PodIP,
			strconv.Itoa(int(run.Spec.Delivery.Port)),
		),
		Path: deliveryv1alpha1.EndpointPath,
	}
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		endpoint.String(),
		bytes.NewReader(payload),
	)
	if err != nil {
		return resultv1alpha1.Envelope{}, false, fmt.Errorf("build Service delivery request: %w", err)
	}
	request.Host = pod.Name
	request.Header.Set("Content-Type", deliveryContentTypeJSON)
	request.Header.Set("Accept", deliveryContentTypeJSON)

	response, err := r.deliveryClient().Do(request)
	if err != nil {
		return resultv1alpha1.Envelope{}, true, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, deliveryv1alpha1.MaxResponseBytes+1))
	if err != nil {
		return resultv1alpha1.Envelope{}, true, fmt.Errorf("read Service delivery response: %w", err)
	}
	if len(body) > deliveryv1alpha1.MaxResponseBytes {
		return resultv1alpha1.Envelope{}, false, fmt.Errorf(
			"Service delivery response exceeded %d bytes.",
			deliveryv1alpha1.MaxResponseBytes,
		)
	}
	if response.StatusCode != http.StatusOK {
		return resultv1alpha1.Envelope{}, false, fmt.Errorf(
			"Service delivery returned HTTP %d.",
			response.StatusCode,
		)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != deliveryContentTypeJSON {
		return resultv1alpha1.Envelope{}, false, fmt.Errorf(
			"Service delivery response Content-Type must be application/json.",
		)
	}
	envelope, err := resultv1alpha1.ParseVersioned(string(body))
	if err != nil {
		return resultv1alpha1.Envelope{}, false, fmt.Errorf(
			"Service delivery response is invalid: %v.",
			err,
		)
	}
	return envelope, false, nil
}

func (r *AgentRunReconciler) deliveryClient() HTTPDoer {
	if r.DeliveryClient != nil {
		return r.DeliveryClient
	}
	return defaultDeliveryClient
}

func newDefaultDeliveryClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (r *AgentRunReconciler) setDeliveryWaiting(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	reason string,
	message string,
) error {
	return r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = kontextv1alpha1.AgentRunPhasePending
		next.Message = message
		next.CompletionTime = nil
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			deliveryConditions(reason, message, true)...,
		)
	})
}

func (r *AgentRunReconciler) setDeliveryRunning(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	podName string,
) error {
	return r.patchRunStatus(ctx, run, func(next *kontextv1alpha1.AgentRunStatus) {
		next.Phase = kontextv1alpha1.AgentRunPhaseRunning
		next.PodName = podName
		next.Message = fmt.Sprintf("Delivering agent run to standing Service Pod %s.", podName)
		if next.StartTime == nil {
			next.StartTime = r.nowPtr()
		}
		next.CompletionTime = nil
		setStatusConditions(
			&next.Conditions,
			run.Generation,
			deliveryConditions("Delivering", next.Message, true)...,
		)
	})
}

func deliveryConditions(reason string, message string, progressing bool) []metav1.Condition {
	progressingStatus := metav1.ConditionFalse
	if progressing {
		progressingStatus = metav1.ConditionTrue
	}
	return []metav1.Condition{{
		Type:    conditions.Complete,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	}, {
		Type:    conditions.Progressing,
		Status:  progressingStatus,
		Reason:  reason,
		Message: message,
	}}
}

func (r *AgentRunReconciler) deliveryDeadline(
	run *kontextv1alpha1.AgentRun,
	wallclockLimit *time.Duration,
	now time.Time,
) (time.Time, bool) {
	deadline := run.CreationTimestamp.Time.Add(defaultDeliveryTimeout)
	if run.CreationTimestamp.IsZero() {
		deadline = now.Add(defaultDeliveryTimeout)
	}
	if wallclockLimit == nil || run.Status.StartTime == nil {
		return deadline, false
	}
	wallclockDeadline := run.Status.StartTime.Add(*wallclockLimit)
	if !wallclockDeadline.After(deadline) {
		return wallclockDeadline, true
	}
	return deadline, false
}

func (r *AgentRunReconciler) failDeliveryTimeout(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
) error {
	return r.transitionRun(
		ctx,
		run,
		kontextv1alpha1.AgentRunPhaseFailed,
		fmt.Sprintf("Service delivery timed out after %s.", defaultDeliveryTimeout),
		nil,
	)
}

func (r *AgentRunReconciler) exceedDeliveryBudget(
	ctx context.Context,
	run *kontextv1alpha1.AgentRun,
	limit time.Duration,
) error {
	return r.transitionRun(
		ctx,
		run,
		kontextv1alpha1.AgentRunPhaseBudgetExceeded,
		fmt.Sprintf("Wallclock budget exceeded after %s.", limit),
		nil,
	)
}

func (r *AgentRunReconciler) deliveryBackoff(run *kontextv1alpha1.AgentRun) time.Duration {
	elapsed := r.now().Sub(run.CreationTimestamp.Time)
	switch {
	case elapsed >= 2*time.Minute:
		return maxDeliveryBackoff
	case elapsed >= 30*time.Second:
		return 5 * time.Second
	case elapsed >= 5*time.Second:
		return 2 * time.Second
	default:
		return initialDeliveryBackoff
	}
}
