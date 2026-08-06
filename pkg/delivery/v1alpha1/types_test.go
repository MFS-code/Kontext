package v1alpha1_test

import (
	"strings"
	"testing"

	deliveryv1alpha1 "github.com/MFS-code/Kontext/pkg/delivery/v1alpha1"
	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
)

func TestDeliveryContractConstants(t *testing.T) {
	if deliveryv1alpha1.APIVersion != "kontext.dev/delivery/v1alpha1" {
		t.Fatalf("apiVersion = %q", deliveryv1alpha1.APIVersion)
	}
	if deliveryv1alpha1.EndpointPath != "/kontext.dev/v1alpha1/agent-runs" {
		t.Fatalf("endpoint path = %q", deliveryv1alpha1.EndpointPath)
	}
	if deliveryv1alpha1.MaxResponseBytes != resultv1alpha1.MaxTerminationMessageBytes {
		t.Fatalf(
			"response limit = %d, result limit = %d",
			deliveryv1alpha1.MaxResponseBytes,
			resultv1alpha1.MaxTerminationMessageBytes,
		)
	}
}

func TestParseDeliveryRequest(t *testing.T) {
	request, err := deliveryv1alpha1.Parse([]byte(`{
		"apiVersion":"kontext.dev/delivery/v1alpha1",
		"run":{"name":"review-1","namespace":"default","uid":"run-uid"},
		"goal":"Review the change."
	}`))
	if err != nil {
		t.Fatalf("parse delivery request: %v", err)
	}
	if request.Run.Name != "review-1" ||
		request.Run.Namespace != "default" ||
		request.Run.UID != "run-uid" ||
		request.Goal != "Review the change." {
		t.Fatalf("decoded request = %#v", request)
	}
}

func TestParseDeliveryRequestRejectsInvalidRecords(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "wrong version",
			body: `{"apiVersion":"future","run":{"name":"run","namespace":"default","uid":"uid"},"goal":"work"}`,
			want: "unsupported delivery apiVersion",
		},
		{
			name: "missing identity",
			body: `{"apiVersion":"kontext.dev/delivery/v1alpha1","run":{},"goal":"work"}`,
			want: "delivery run name is required",
		},
		{
			name: "missing goal",
			body: `{"apiVersion":"kontext.dev/delivery/v1alpha1","run":{"name":"run","namespace":"default","uid":"uid"}}`,
			want: "delivery goal is required",
		},
		{
			name: "unknown field",
			body: `{"apiVersion":"kontext.dev/delivery/v1alpha1","run":{"name":"run","namespace":"default","uid":"uid"},"goal":"work","extra":true}`,
			want: "unknown field",
		},
		{
			name: "unknown identity field",
			body: `{"apiVersion":"kontext.dev/delivery/v1alpha1","run":{"name":"run","namespace":"default","uid":"uid","extra":true},"goal":"work"}`,
			want: "unknown field",
		},
		{
			name: "trailing JSON",
			body: `{"apiVersion":"kontext.dev/delivery/v1alpha1","run":{"name":"run","namespace":"default","uid":"uid"},"goal":"work"} {}`,
			want: "trailing JSON value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := deliveryv1alpha1.Parse([]byte(test.body))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want text %q", err, test.want)
			}
		})
	}
}
