package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	eventv1alpha1 "github.com/MFS-code/Kontext/pkg/event/v1alpha1"
)

const (
	MaxLogBytes       = 2 << 20
	MaxEventCount     = 1000
	MaxEventLineBytes = 64 << 10
)

type parsedLogs struct {
	Events EventSummary
	Errors []string
}

func ParseLogs(
	logs []byte,
	truncated bool,
	relevantTypes map[eventv1alpha1.Type]struct{},
	detailTypes map[eventv1alpha1.Type]struct{},
) parsedLogs {
	parsed := parsedLogs{
		Events: EventSummary{
			Counts:    make(map[eventv1alpha1.Type]int),
			Truncated: truncated,
		},
	}
	if len(logs) > MaxLogBytes {
		logs = logs[len(logs)-MaxLogBytes:]
		parsed.Events.Truncated = true
		parsed.Errors = append(parsed.Errors, "runtime logs exceeded collection limit")
	}
	if truncated {
		parsed.Errors = append(parsed.Errors, "runtime log tail is incomplete")
	}
	metadataLimitReported := false
	scanner := bufio.NewScanner(bytes.NewReader(logs))
	scanner.Buffer(make([]byte, 4096), MaxEventLineBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		event, err := eventv1alpha1.Parse(line)
		if err != nil {
			if malformedRelevantEvent(line, relevantTypes) {
				parsed.Errors = append(parsed.Errors, fmt.Sprintf("parse required runtime event: %v", err))
			}
			continue
		}
		if _, relevant := relevantTypes[event.Type]; !relevant {
			continue
		}
		if err := validateEventData(event); err != nil {
			parsed.Errors = append(
				parsed.Errors,
				fmt.Sprintf("parse required %s event data: %v", event.Type, err),
			)
			continue
		}
		parsed.Events.Counts[event.Type]++
		if len(parsed.Events.Metadata) >= MaxEventCount {
			parsed.Events.Truncated = true
			if !metadataLimitReported {
				parsed.Errors = append(parsed.Errors, "required runtime event metadata exceeded collection limit")
				metadataLimitReported = true
			}
			continue
		}
		if _, detailsRequired := detailTypes[event.Type]; detailsRequired {
			summarizeEvent(&parsed.Events, event)
		}
	}
	if err := scanner.Err(); err != nil {
		parsed.Errors = append(parsed.Errors, fmt.Sprintf("scan runtime logs: %v", err))
	}
	return parsed
}

func malformedRelevantEvent(line []byte, relevantTypes map[eventv1alpha1.Type]struct{}) bool {
	var header struct {
		APIVersion string             `json:"apiVersion"`
		Type       eventv1alpha1.Type `json:"type"`
	}
	if err := json.Unmarshal(line, &header); err == nil {
		_, relevant := relevantTypes[header.Type]
		return relevant && strings.HasPrefix(header.APIVersion, "kontext.dev/event/")
	}
	normalized := normalizeMalformedFrame(line)
	versionMarker := []byte(`"apiVersion":"` + eventv1alpha1.APIVersion + `"`)
	if !bytes.Contains(normalized, versionMarker) {
		return false
	}
	for eventType := range relevantTypes {
		marker := []byte(`"type":"` + string(eventType) + `"`)
		if bytes.Contains(normalized, marker) {
			return true
		}
	}
	return len(relevantTypes) > 0 && bytes.Contains(normalized, []byte(`"type"`))
}

func normalizeMalformedFrame(line []byte) []byte {
	normalized := make([]byte, 0, len(line))
	for _, value := range line {
		switch value {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			continue
		default:
			normalized = append(normalized, value)
		}
	}
	return bytes.ReplaceAll(normalized, []byte(`\/`), []byte(`/`))
}

func validateEventData(event eventv1alpha1.Event) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Data, &fields); err != nil {
		return fmt.Errorf("data must be an object: %w", err)
	}
	if fields == nil {
		return fmt.Errorf("data must be an object")
	}
	switch event.Type {
	case eventv1alpha1.TypeLifecycle:
		if _, err := requiredString(fields, "phase"); err != nil {
			return err
		}
	case eventv1alpha1.TypeTool:
		if _, err := requiredString(fields, "name"); err != nil {
			return err
		}
		count, err := requiredInt32(fields, "count")
		if err != nil {
			return err
		}
		if count < 1 {
			return fmt.Errorf("count must be at least 1")
		}
		if _, err := requiredBool(fields, "isError"); err != nil {
			return err
		}
		if _, err := requiredBool(fields, "truncated"); err != nil {
			return err
		}
		if value, exists := fields["errorCode"]; exists {
			var errorCode string
			if err := json.Unmarshal(value, &errorCode); err != nil {
				return fmt.Errorf("errorCode must be a string")
			}
		}
	case eventv1alpha1.TypeError:
		if _, err := requiredString(fields, "code"); err != nil {
			return err
		}
		if _, err := requiredString(fields, "message"); err != nil {
			return err
		}
	case eventv1alpha1.TypeOutput:
		if _, err := requiredString(fields, "mediaType"); err != nil {
			return err
		}
		if _, exists := fields["value"]; !exists {
			return fmt.Errorf("value is required")
		}
	case eventv1alpha1.TypeUsage:
		turn, err := requiredInt32(fields, "turn")
		if err != nil {
			return err
		}
		if turn < 1 {
			return fmt.Errorf("turn must be at least 1")
		}
		var usage map[string]json.RawMessage
		if err := json.Unmarshal(fields["usage"], &usage); err != nil || usage == nil {
			return fmt.Errorf("usage must be an object")
		}
	}
	return nil
}

func summarizeEvent(summary *EventSummary, event eventv1alpha1.Event) {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(event.Data, &fields)
	metadata := EventMetadata{Timestamp: event.Timestamp, Type: event.Type}
	switch event.Type {
	case eventv1alpha1.TypeLifecycle:
		metadata.Phase, _ = requiredString(fields, "phase")
		if metadata.Phase != "" {
			summary.Lifecycle = append(summary.Lifecycle, metadata.Phase)
		}
	case eventv1alpha1.TypeTool:
		tool := ToolEvent{
			ErrorCode: optionalString(fields, "errorCode"),
		}
		tool.Name, _ = requiredString(fields, "name")
		tool.Count, _ = requiredInt32(fields, "count")
		tool.IsError, _ = requiredBool(fields, "isError")
		tool.Truncated, _ = requiredBool(fields, "truncated")
		summary.Tools = append(summary.Tools, tool)
		metadata.Name = tool.Name
		metadata.IsError = tool.IsError
		metadata.ErrorCode = tool.ErrorCode
		metadata.Truncated = tool.Truncated
	case eventv1alpha1.TypeError:
		metadata.ErrorCode, _ = requiredString(fields, "code")
		if metadata.ErrorCode != "" {
			summary.Errors = append(summary.Errors, metadata.ErrorCode)
		}
	case eventv1alpha1.TypeOutput, eventv1alpha1.TypeUsage:
	}
	summary.Metadata = append(summary.Metadata, metadata)
}

func requiredString(fields map[string]json.RawMessage, name string) (string, error) {
	value, exists := fields[name]
	if !exists {
		return "", fmt.Errorf("%s is required", name)
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	if strings.TrimSpace(decoded) == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return decoded, nil
}

func requiredInt32(fields map[string]json.RawMessage, name string) (int32, error) {
	value, exists := fields[name]
	if !exists {
		return 0, fmt.Errorf("%s is required", name)
	}
	var decoded int32
	if err := json.Unmarshal(value, &decoded); err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return decoded, nil
}

func requiredBool(fields map[string]json.RawMessage, name string) (bool, error) {
	value, exists := fields[name]
	if !exists {
		return false, fmt.Errorf("%s is required", name)
	}
	var decoded bool
	if err := json.Unmarshal(value, &decoded); err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return decoded, nil
}

func optionalString(fields map[string]json.RawMessage, name string) string {
	var decoded string
	_ = json.Unmarshal(fields[name], &decoded)
	return decoded
}

func boundedMessage(value string) string {
	value = strings.TrimSpace(value)
	return boundedString(value, 4096)
}

func boundedString(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) > limit {
		end := limit
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
		return value[:end]
	}
	return value
}
