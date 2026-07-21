package admission

import (
	"context"
	"encoding/json"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	webhookadmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/runfactory"
)

const DefaultWebhookPath = "/mutate-kontext-dev-v1alpha1-agentrun"

type TaskHandler struct {
	reader client.Reader
	scheme *runtime.Scheme
}

func (h *TaskHandler) Handle(ctx context.Context, request webhookadmission.Request) webhookadmission.Response {
	var invocation kontextv1alpha1.AgentRun
	if err := json.Unmarshal(request.Object.Raw, &invocation); err != nil {
		return webhookadmission.Errored(http.StatusBadRequest, err)
	}

	referenceName := ""
	if invocation.Spec.AgentRef != nil {
		referenceName = invocation.Spec.AgentRef.Name
	}
	if referenceName == "" {
		return webhookadmission.Allowed("AgentRun does not reference a Task Agent")
	}
	namespace := request.Namespace
	if namespace == "" {
		namespace = invocation.Namespace
	}
	invocation.Namespace = namespace
	if len(invocation.Spec.Parameters) == 0 {
		invocation.Spec.Parameters = nil
	}

	var agent kontextv1alpha1.Agent
	err := h.reader.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      referenceName,
	}, &agent)
	if apierrors.IsNotFound(err) {
		return webhookadmission.Denied((&runfactory.ResolutionError{
			Code:      runfactory.ErrorMissingAgent,
			AgentName: referenceName,
		}).Error())
	}
	if err != nil {
		return webhookadmission.Errored(http.StatusInternalServerError, err)
	}

	resolved, err := runfactory.ResolveTask(&agent, &invocation, h.scheme)
	if err != nil {
		return webhookadmission.Denied(err.Error())
	}
	mutated, err := json.Marshal(resolved)
	if err != nil {
		return webhookadmission.Errored(http.StatusInternalServerError, err)
	}
	return webhookadmission.PatchResponseFromRaw(request.Object.Raw, mutated)
}

func Handler(reader client.Reader, scheme *runtime.Scheme) http.Handler {
	return &webhookadmission.Webhook{Handler: &TaskHandler{reader: reader, scheme: scheme}}
}
