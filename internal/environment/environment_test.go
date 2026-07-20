package environment

import (
	"slices"
	"testing"
)

func TestSortedOrdersEnvironmentNames(t *testing.T) {
	got := Sorted(map[string]string{
		"ZED":    "last",
		"ALPHA":  "first",
		"MIDDLE": "middle",
	})
	want := []string{"ALPHA=first", "MIDDLE=middle", "ZED=last"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}
