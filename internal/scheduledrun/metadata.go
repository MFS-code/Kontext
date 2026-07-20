// Package scheduledrun owns persisted metadata used to identify scheduled
// AgentRun slots and recover their monotonic creation sequence.
package scheduledrun

import (
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// SlotAnnotationKey records the cron slot that produced an AgentRun.
	SlotAnnotationKey = "kontext.dev/scheduled-slot"
	// SequenceAnnotationKey records the Agent's monotonic run count.
	SequenceAnnotationKey = "kontext.dev/scheduled-sequence"
)

// SetMetadata stamps the canonical slot and sequence annotations.
func SetMetadata(object metav1.Object, slot time.Time, sequence int32) {
	annotations := object.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[SlotAnnotationKey] = slot.UTC().Format(time.RFC3339)
	annotations[SequenceAnnotationKey] = strconv.FormatInt(int64(sequence), 10)
	object.SetAnnotations(annotations)
}

// Slot reads a valid scheduled slot from an object.
func Slot(object metav1.Object) (time.Time, bool) {
	slot, err := time.Parse(time.RFC3339, object.GetAnnotations()[SlotAnnotationKey])
	return slot, err == nil
}

// Sequence reads a positive scheduled creation sequence from an object.
func Sequence(object metav1.Object) (int32, bool) {
	value, err := strconv.ParseInt(object.GetAnnotations()[SequenceAnnotationKey], 10, 32)
	if err != nil || value < 1 {
		return 0, false
	}
	return int32(value), true
}

// RepresentsSlot reports whether an object carries the expected slot.
func RepresentsSlot(object metav1.Object, slot time.Time) bool {
	observed, ok := Slot(object)
	return ok && observed.Equal(slot)
}
