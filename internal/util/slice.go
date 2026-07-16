package util

// CloneSlice returns a non-nil shallow copy of in.
func CloneSlice[T any](in []T) []T {
	return append([]T{}, in...)
}
