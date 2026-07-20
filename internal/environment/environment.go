// Package environment provides deterministic process environment encoding.
package environment

import "sort"

// Sorted returns name=value entries ordered by variable name.
func Sorted(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	encoded := make([]string, 0, len(names))
	for _, name := range names {
		encoded = append(encoded, name+"="+values[name])
	}
	return encoded
}
