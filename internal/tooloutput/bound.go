// Package tooloutput applies the shared byte-bound contract for tool results.
package tooloutput

import (
	"encoding/json"
	"unicode/utf8"
)

const emptyPartial = `{"partial":""}`

// Bound returns value unchanged when it is valid UTF-8 and within maxBytes.
// Truncated values become a JSON partial envelope. Positive limits too small
// for that envelope receive the smallest deterministic JSON value that fits.
func Bound(value string, maxBytes int64) (content string, truncated bool) {
	if utf8.ValidString(value) && int64(len(value)) <= maxBytes {
		return value, false
	}
	if maxBytes <= 0 {
		return "", true
	}
	if maxBytes == 1 {
		return "0", true
	}
	if maxBytes < int64(len(emptyPartial)) {
		return "{}", true
	}

	low, high := 0, len(value)
	best := emptyPartial
	for low <= high {
		middle := low + (high-low)/2
		prefix := TruncateUTF8(value, int64(middle))
		encoded, err := json.Marshal(struct {
			Partial string `json:"partial"`
		}{Partial: prefix})
		if err == nil && int64(len(encoded)) <= maxBytes {
			best = string(encoded)
			low = middle + 1
			continue
		}
		high = middle - 1
	}
	return best, true
}

// TruncateUTF8 returns the longest valid UTF-8 prefix within maxBytes.
func TruncateUTF8(value string, maxBytes int64) string {
	if maxBytes <= 0 {
		return ""
	}
	if int64(len(value)) <= maxBytes && utf8.ValidString(value) {
		return value
	}
	end := len(value)
	if int64(end) > maxBytes {
		end = int(maxBytes)
	}
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}
