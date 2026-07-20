// Package v1alpha1 contains API Schema definitions for the kontext v1alpha1
// API group.
//
// CRDs evolve additively: existing fields retain their meaning and new
// optional fields may be introduced within the version. Arbitrary JSON is
// accepted only by fields explicitly documented and marked schemaless, such as
// AgentRun status output values; every other field remains structurally
// validated by Kubernetes.
//
// +kubebuilder:object:generate=true
// +groupName=kontext.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion = schema.GroupVersion{Group: "kontext.dev", Version: "v1alpha1"}

	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	AddToScheme = SchemeBuilder.AddToScheme
)
