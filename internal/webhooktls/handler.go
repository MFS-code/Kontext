package webhooktls

import (
	"context"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type PlumbingHandler struct{}

func (PlumbingHandler) Handle(_ context.Context, _ admission.Request) admission.Response {
	return admission.Allowed("Task mutation is reserved for issue #84")
}

func Handler() http.Handler {
	return &admission.Webhook{Handler: PlumbingHandler{}}
}
