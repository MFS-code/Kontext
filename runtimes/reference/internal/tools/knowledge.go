package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	runtimeapi "github.com/kontext-dev/kontext/runtimes/reference/internal/runtimeapi"
)

const defaultKnowledgeRoot = "/kontext/knowledge"

type knowledgeTool struct {
	root     string
	maxBytes int64
}

type readKnowledgeArguments struct {
	Path string `json:"path"`
}

func newKnowledgeTool(root string, maxBytes int64) *knowledgeTool {
	if strings.TrimSpace(root) == "" {
		root = defaultKnowledgeRoot
	}
	return &knowledgeTool{root: root, maxBytes: maxBytes}
}

func (tool *knowledgeTool) Definition() runtimeapi.ToolDefinition {
	return runtimeapi.ToolDefinition{
		Name:        NameReadKnowledge,
		Description: "Read one UTF-8 text file from the mounted Kontext knowledge directory.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{"path":{"type":"string","description":"Relative file path under /kontext/knowledge"}},
			"required":["path"],
			"additionalProperties":false
		}`),
	}
}

func (tool *knowledgeTool) Execute(
	ctx context.Context,
	rawArguments []byte,
) (outcome, error) {
	if err := ctx.Err(); err != nil {
		return outcome{}, err
	}
	var arguments readKnowledgeArguments
	if err := decodeArguments(rawArguments, &arguments); err != nil {
		return outcome{}, err
	}
	requested := strings.TrimSpace(arguments.Path)
	if requested == "" {
		return outcome{}, &Error{
			Code:    "invalid_tool_arguments",
			Message: "path must be a non-empty file path",
		}
	}
	if filepath.IsAbs(requested) {
		return outcome{}, &Error{
			Code:    "knowledge_path_denied",
			Message: "path must be relative to /kontext/knowledge",
		}
	}
	cleaned := filepath.Clean(requested)
	if cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return outcome{}, &Error{
			Code:    "knowledge_path_denied",
			Message: "path must remain inside /kontext/knowledge",
		}
	}

	resolvedRoot, err := filepath.EvalSymlinks(tool.root)
	if err != nil {
		return outcome{}, &Error{
			Code:    "knowledge_unavailable",
			Message: fmt.Sprintf("knowledge directory is unavailable: %v", err),
		}
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, cleaned))
	if err != nil {
		if os.IsNotExist(err) {
			return outcome{}, &Error{
				Code:    "knowledge_not_found",
				Message: fmt.Sprintf("knowledge file %q does not exist", requested),
			}
		}
		return outcome{}, &Error{
			Code:    "knowledge_read_failed",
			Message: fmt.Sprintf("resolve knowledge file: %v", err),
		}
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return outcome{}, &Error{
			Code:    "knowledge_path_denied",
			Message: "resolved path leaves /kontext/knowledge",
		}
	}

	file, err := os.Open(resolvedPath)
	if err != nil {
		return outcome{}, &Error{
			Code:    "knowledge_read_failed",
			Message: fmt.Sprintf("open knowledge file: %v", err),
		}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return outcome{}, &Error{
			Code:    "knowledge_read_failed",
			Message: fmt.Sprintf("inspect knowledge file: %v", err),
		}
	}
	if !info.Mode().IsRegular() {
		return outcome{}, &Error{
			Code:    "knowledge_path_denied",
			Message: "path must identify a regular file",
		}
	}

	data, err := io.ReadAll(io.LimitReader(file, tool.maxBytes+utf8.UTFMax))
	if err != nil {
		return outcome{}, &Error{
			Code:    "knowledge_read_failed",
			Message: fmt.Sprintf("read knowledge file: %v", err),
		}
	}
	truncated := int64(len(data)) > tool.maxBytes
	if truncated {
		end := int(tool.maxBytes)
		minimum := end - (utf8.UTFMax - 1)
		if minimum < 0 {
			minimum = 0
		}
		for end > minimum && !utf8.Valid(data[:end]) {
			end--
		}
		if !utf8.Valid(data[:end]) {
			return outcome{}, &Error{
				Code:    "knowledge_invalid_encoding",
				Message: "knowledge file must contain UTF-8 text",
			}
		}
		data = data[:end]
	}
	if !utf8.Valid(data) {
		return outcome{}, &Error{
			Code:    "knowledge_invalid_encoding",
			Message: "knowledge file must contain UTF-8 text",
		}
	}
	return outcome{
		Content:   string(data),
		Truncated: truncated,
	}, nil
}

func decodeArguments(raw []byte, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return &Error{
			Code:    "invalid_tool_arguments",
			Message: fmt.Sprintf("invalid tool arguments: %v", err),
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &Error{
			Code:    "invalid_tool_arguments",
			Message: "tool arguments contain trailing data",
		}
	}
	return nil
}
