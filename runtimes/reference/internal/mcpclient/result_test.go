package mcpclient

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNormalizeResultDelegatesInvalidUTF8Bounding(t *testing.T) {
	content, truncated, resultErr := normalizeResult(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "ok\xfftail"}},
	}, newRedactor(nil))
	if resultErr != nil {
		t.Fatalf("normalize result: %v", resultErr)
	}
	if !truncated || !json.Valid([]byte(content)) || !utf8.ValidString(content) {
		t.Fatalf("bounded result = %q truncated=%t", content, truncated)
	}
	var envelope struct {
		Partial string `json:"partial"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil || envelope.Partial != "ok" {
		t.Fatalf("unexpected partial envelope %q: %#v err=%v", content, envelope, err)
	}
}

func TestNormalizeResultRejectsUnsupportedContent(t *testing.T) {
	_, truncated, resultErr := normalizeResult(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png"}},
	}, newRedactor(nil))
	if resultErr == nil || resultErr.Code != "mcp_unsupported_content" || truncated {
		t.Fatalf("unexpected unsupported-content result: err=%#v truncated=%t", resultErr, truncated)
	}
}
