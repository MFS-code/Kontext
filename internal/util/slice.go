package util

// CloneSlice returns a non-nil shallow copy to preserve API construction semantics.
func CloneSlice[T any](in []T) []T {
	return append([]T{}, in...)
}
