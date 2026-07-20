package tooloutput

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBound(t *testing.T) {
	tests := []struct {
		name          string
		value         string
		maxBytes      int64
		want          string
		wantTruncated bool
		wantPartial   bool
	}{
		{
			name:     "plain text within limit",
			value:    "hello",
			maxBytes: 5,
			want:     "hello",
		},
		{
			name:     "JSON within limit",
			value:    `{"ok":true}`,
			maxBytes: 11,
			want:     `{"ok":true}`,
		},
		{
			name:          "one byte uses JSON number",
			value:         "oversized",
			maxBytes:      1,
			want:          "0",
			wantTruncated: true,
		},
		{
			name:          "tiny limit uses JSON object",
			value:         "oversized",
			maxBytes:      2,
			want:          "{}",
			wantTruncated: true,
		},
		{
			name:          "plain text gets partial envelope",
			value:         strings.Repeat("a", 64),
			maxBytes:      24,
			wantTruncated: true,
			wantPartial:   true,
		},
		{
			name:          "JSON gets partial envelope",
			value:         `{"status":"ok","items":[1,2,3]}`,
			maxBytes:      25,
			wantTruncated: true,
			wantPartial:   true,
		},
		{
			name:          "escaping counts toward envelope",
			value:         strings.Repeat(`"\`, 32),
			maxBytes:      22,
			wantTruncated: true,
			wantPartial:   true,
		},
		{
			name:          "multibyte boundary stays valid",
			value:         strings.Repeat("é", 32),
			maxBytes:      24,
			wantTruncated: true,
			wantPartial:   true,
		},
		{
			name:          "invalid UTF-8 is removed",
			value:         "ok\xfftail",
			maxBytes:      24,
			wantTruncated: true,
			wantPartial:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, truncated := Bound(test.value, test.maxBytes)
			if truncated != test.wantTruncated {
				t.Fatalf("truncated=%t want %t; content=%q", truncated, test.wantTruncated, got)
			}
			if test.want != "" && got != test.want {
				t.Fatalf("content=%q want %q", got, test.want)
			}
			if int64(len(got)) > test.maxBytes {
				t.Fatalf("content is %d bytes, limit is %d", len(got), test.maxBytes)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("content is not valid UTF-8: %q", got)
			}
			if !truncated {
				return
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("truncated content is not valid JSON: %q", got)
			}
			if !test.wantPartial {
				return
			}
			var envelope struct {
				Partial string `json:"partial"`
			}
			if err := json.Unmarshal([]byte(got), &envelope); err != nil {
				t.Fatalf("decode partial envelope: %v", err)
			}
			if envelope.Partial == "" || !strings.HasPrefix(test.value, envelope.Partial) {
				t.Fatalf("partial %q is not a non-empty input prefix", envelope.Partial)
			}
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		maxBytes int64
		want     string
	}{
		{name: "disabled", value: "abc", maxBytes: 0, want: ""},
		{name: "unchanged", value: "aéb", maxBytes: 4, want: "aéb"},
		{name: "ASCII", value: "abcd", maxBytes: 3, want: "abc"},
		{name: "multibyte boundary", value: "aéb", maxBytes: 2, want: "a"},
		{name: "invalid suffix", value: "ok\xfftail", maxBytes: 8, want: "ok"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := TruncateUTF8(test.value, test.maxBytes); got != test.want {
				t.Fatalf("TruncateUTF8(%q, %d)=%q want %q", test.value, test.maxBytes, got, test.want)
			}
		})
	}
}
