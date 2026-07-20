package mcpclient

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCountingReadCloserEnforcesWireLimit(t *testing.T) {
	reader := &countingReadCloser{
		body:  io.NopCloser(strings.NewReader("12345")),
		limit: 4,
	}
	content, err := io.ReadAll(reader)
	if string(content) != "1234" || !errors.Is(err, errMCPHTTPWireLimit) {
		t.Fatalf("read = (%q, %v), want bounded wire-limit failure", content, err)
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, errMCPHTTPWireLimit) {
		t.Fatalf("subsequent read error = %v, want wire-limit failure", err)
	}
}

func TestSameOriginRedirectPolicyRejectsCrossOriginAndRedirectLoops(t *testing.T) {
	endpoint := mustParseURL(t, "https://example.test/mcp")
	policy := sameOriginRedirectPolicy(endpoint)
	if err := policy(
		&http.Request{URL: mustParseURL(t, "https://other.test/mcp")},
		nil,
	); err == nil {
		t.Fatal("cross-origin redirect was accepted")
	}
	via := make([]*http.Request, 10)
	if err := policy(&http.Request{URL: endpoint}, via); err == nil {
		t.Fatal("redirect loop limit was not enforced")
	}
}
