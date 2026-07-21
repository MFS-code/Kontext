package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func patchStatus(ctx context.Context, c client.Client, obj client.Object, mutate func()) error {
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	mutate()
	return c.Status().Patch(ctx, obj, patch)
}

func setStatusConditions(existing *[]metav1.Condition, generation int64, updates ...metav1.Condition) {
	*existing = append([]metav1.Condition(nil), (*existing)...)
	for i := range updates {
		updates[i].ObservedGeneration = generation
		meta.SetStatusCondition(existing, updates[i])
	}
}
