package webhooktls

import (
	"context"
	"encoding/json"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/runfactory"
)

type TaskHandler struct {
	reader client.Reader
	scheme *runtime.Scheme
}

func (h *TaskHandler) Handle(ctx context.Context, request admission.Request) admission.Response {
	var invocation kontextv1alpha1.AgentRun
	if err := json.Unmarshal(request.Object.Raw, &invocation); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	referenceName := ""
	if invocation.Spec.AgentRef != nil {
		referenceName = invocation.Spec.AgentRef.Name
	}
	if referenceName == "" {
		return admission.Allowed("AgentRun does not reference a Task Agent")
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
		return admission.Denied((&runfactory.ResolutionError{
			Code:      runfactory.ErrorMissingAgent,
			AgentName: referenceName,
		}).Error())
	}
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	resolved, err := runfactory.ResolveTask(&agent, &invocation, h.scheme)
	if err != nil {
		return admission.Denied(err.Error())
	}
	mutated, err := json.Marshal(resolved)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(request.Object.Raw, mutated)
}

func Handler(reader client.Reader, scheme *runtime.Scheme) http.Handler {
	return &admission.Webhook{Handler: &TaskHandler{reader: reader, scheme: scheme}}
}
