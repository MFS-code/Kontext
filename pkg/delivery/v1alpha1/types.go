// Package v1alpha1 defines the warm-delivery request contract shared by
// Service runtimes and the control plane.
//
// Delivery requests are strict records. Unknown top-level, run identity, or
// target identity fields require a new contract version instead of being
// silently ignored.
package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

const (
	APIVersion   = "kontext.dev/delivery/v1alpha1"
	EndpointPath = "/kontext.dev/v1alpha1/agent-runs"

	MaxResponseBytes = resultv1alpha1.MaxTerminationMessageBytes
)

// Request delivers one fully resolved AgentRun goal to a standing Service
// runtime. Run.UID is the idempotency identity for the delivery.
type Request struct {
	APIVersion string      `json:"apiVersion"`
	Run        RunIdentity `json:"run"`
	Target     PodIdentity `json:"target"`
	Goal       string      `json:"goal"`
}

// RunIdentity identifies the immutable Kubernetes audit record for a delivery.
type RunIdentity struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	UID       string `json:"uid"`
}

// PodIdentity binds a delivery attempt to the verified standing Service Pod.
type PodIdentity struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// Validate rejects requests that cannot be tied to one persisted AgentRun.
func (request Request) Validate() error {
	if request.APIVersion != APIVersion {
		return fmt.Errorf("unsupported delivery apiVersion %q", request.APIVersion)
	}
	if request.Run.Name == "" {
		return errors.New("delivery run name is required")
	}
	if request.Run.Namespace == "" {
		return errors.New("delivery run namespace is required")
	}
	if request.Run.UID == "" {
		return errors.New("delivery run uid is required")
	}
	if request.Target.Name == "" {
		return errors.New("delivery target pod name is required")
	}
	if request.Target.UID == "" {
		return errors.New("delivery target pod uid is required")
	}
	if request.Goal == "" {
		return errors.New("delivery goal is required")
	}
	return nil
}

// Parse decodes one strict delivery request with no trailing JSON.
func Parse(data []byte) (Request, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode delivery request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Request{}, errors.New("decode delivery request: trailing JSON value")
		}
		return Request{}, fmt.Errorf("decode delivery request trailing data: %w", err)
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}
