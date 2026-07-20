package scheduledrun_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MFS-code/Kontext/internal/scheduledrun"
)

func TestMetadataKeysRoundTripIndependently(t *testing.T) {
	slot := time.Date(2026, time.July, 20, 12, 5, 0, 0, time.FixedZone("test", 2*60*60))
	object := &metav1.PartialObjectMetadata{}
	scheduledrun.SetMetadata(object, slot, 7)

	if scheduledrun.SlotAnnotationKey == "" ||
		scheduledrun.SequenceAnnotationKey == "" ||
		scheduledrun.SlotAnnotationKey == scheduledrun.SequenceAnnotationKey {
		t.Fatalf(
			"scheduled metadata keys must be non-empty and distinct: slot=%q sequence=%q",
			scheduledrun.SlotAnnotationKey,
			scheduledrun.SequenceAnnotationKey,
		)
	}
	if len(object.Annotations) != 2 {
		t.Fatalf("metadata annotations = %#v, want exactly two canonical keys", object.Annotations)
	}
	gotSlot, ok := scheduledrun.Slot(object)
	if !ok || !gotSlot.Equal(slot) || gotSlot.Location() != time.UTC {
		t.Fatalf("slot = %s, %t; want UTC-equivalent %s", gotSlot, ok, slot)
	}
	if sequence, ok := scheduledrun.Sequence(object); !ok || sequence != 7 {
		t.Fatalf("sequence = %d, %t; want 7", sequence, ok)
	}
	if !scheduledrun.RepresentsSlot(object, slot) {
		t.Fatal("canonical metadata did not represent its stamped slot")
	}

	delete(object.Annotations, scheduledrun.SlotAnnotationKey)
	if _, ok := scheduledrun.Slot(object); ok {
		t.Fatal("slot parsed after its canonical key was removed")
	}
	if sequence, ok := scheduledrun.Sequence(object); !ok || sequence != 7 {
		t.Fatalf("removing slot key affected sequence: %d, %t", sequence, ok)
	}
}

func TestSequenceRejectsMissingMalformedAndNonPositiveValues(t *testing.T) {
	for name, value := range map[string]string{
		"missing":   "",
		"malformed": "not-a-number",
		"zero":      "0",
		"negative":  "-1",
	} {
		t.Run(name, func(t *testing.T) {
			object := &metav1.PartialObjectMetadata{}
			if value != "" {
				object.Annotations = map[string]string{
					scheduledrun.SequenceAnnotationKey: value,
				}
			}
			if sequence, ok := scheduledrun.Sequence(object); ok {
				t.Fatalf("sequence = %d, true; want invalid", sequence)
			}
		})
	}
}
