package controller_test

import (
	"context"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func TestGenericEnvAPIValidation(t *testing.T) {
	literal := "literal-value"
	secret := &kontextv1alpha1.EnvVarSource{
		SecretKeyRef: kontextv1alpha1.SecretKeySelector{
			Name: "mcp-auth",
			Key:  "authorization",
		},
	}
	tests := []struct {
		name      string
		env       kontextv1alpha1.EnvVar
		wantValid bool
	}{
		{
			name: "literal only",
			env: kontextv1alpha1.EnvVar{
				Name:  "MCP_AUTH_HEADER",
				Value: &literal,
			},
			wantValid: true,
		},
		{
			name: "Secret only",
			env: kontextv1alpha1.EnvVar{
				Name:      "MCP_AUTH_HEADER",
				ValueFrom: secret,
			},
			wantValid: true,
		},
		{
			name: "literal and Secret",
			env: kontextv1alpha1.EnvVar{
				Name:      "MCP_AUTH_HEADER",
				Value:     &literal,
				ValueFrom: secret,
			},
		},
		{
			name: "neither literal nor Secret",
			env:  kontextv1alpha1.EnvVar{Name: "MCP_AUTH_HEADER"},
		},
	}
	objectFactories := []struct {
		name string
		new  func(string, kontextv1alpha1.EnvVar) client.Object
	}{
		{
			name: "Agent",
			new: func(name string, env kontextv1alpha1.EnvVar) client.Object {
				return &kontextv1alpha1.Agent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: kontextv1alpha1.AgentSpec{
						Mode:    kontextv1alpha1.AgentModeService,
						Goal:    "test generic environment validation",
						Model:   "test/model",
						Runtime: echoRuntimeSpec(),
						Env:     []kontextv1alpha1.EnvVar{env},
					},
				}
			},
		},
		{
			name: "AgentRun",
			new: func(name string, env kontextv1alpha1.EnvVar) client.Object {
				return &kontextv1alpha1.AgentRun{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: kontextv1alpha1.AgentRunSpec{
						Goal:    "test generic environment validation",
						Model:   "test/model",
						Runtime: echoRuntimeSpec(),
						Env:     []kontextv1alpha1.EnvVar{env},
					},
				}
			},
		},
	}

	for objectIndex, objectFactory := range objectFactories {
		for testIndex, test := range tests {
			t.Run(objectFactory.name+"/"+test.name, func(t *testing.T) {
				name := fmt.Sprintf("env-validation-%d-%d", objectIndex, testIndex)
				object := objectFactory.new(name, test.env)
				err := k8sClient.Create(context.Background(), object)
				if test.wantValid {
					if err != nil {
						t.Fatalf("expected valid object: %v", err)
					}
					if err := k8sClient.Delete(context.Background(), object); err != nil {
						t.Fatalf("delete valid object: %v", err)
					}
					return
				}
				if !apierrors.IsInvalid(err) {
					t.Fatalf("expected API validation error, got %v", err)
				}
			})
		}
	}
}
