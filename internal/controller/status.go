package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func patchStatus(ctx context.Context, c client.Client, obj client.Object, mutate func()) error {
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	mutate()
	return c.Status().Patch(ctx, obj, patch)
}

func nowPtr() *metav1.Time {
	now := metav1.Now()
	return &now
}
