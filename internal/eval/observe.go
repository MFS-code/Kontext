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

type lifecycleEventData struct {
	Phase string `json:"phase"`
}

type toolEventData struct {
	Name      string `json:"name"`
	Count     *int32 `json:"count"`
	IsError   *bool  `json:"isError"`
	ErrorCode string `json:"errorCode"`
	Truncated *bool  `json:"truncated"`
}

type errorEventData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type outputEventData struct {
	MediaType string          `json:"mediaType"`
	Value     json.RawMessage `json:"value"`
}

type usageEventData struct {
	Turn  *int32                     `json:"turn"`
	Usage map[string]json.RawMessage `json:"usage"`
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
			if bytes.Contains(line, []byte(eventv1alpha1.APIVersion)) {
				parsed.Errors = append(parsed.Errors, fmt.Sprintf("parse required runtime event: %v", err))
			}
			continue
		}
		if _, relevant := relevantTypes[event.Type]; !relevant {
			continue
		}
		data, err := parseEventData(event)
		if err != nil {
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
			summarizeEvent(&parsed.Events, event, data)
		}
	}
	if err := scanner.Err(); err != nil {
		parsed.Errors = append(parsed.Errors, fmt.Sprintf("scan runtime logs: %v", err))
	}
	return parsed
}

func parseEventData(event eventv1alpha1.Event) (any, error) {
	trimmed := bytes.TrimSpace(event.Data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("data must be an object")
	}
	switch event.Type {
	case eventv1alpha1.TypeLifecycle:
		var data lifecycleEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, fmt.Errorf("decode lifecycle data: %w", err)
		}
		if strings.TrimSpace(data.Phase) == "" {
			return nil, fmt.Errorf("phase is required and must not be empty")
		}
		return data, nil
	case eventv1alpha1.TypeTool:
		var data toolEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, fmt.Errorf("decode tool data: %w", err)
		}
		if strings.TrimSpace(data.Name) == "" {
			return nil, fmt.Errorf("name is required and must not be empty")
		}
		if data.Count == nil {
			return nil, fmt.Errorf("count is required")
		}
		if *data.Count < 1 {
			return nil, fmt.Errorf("count must be at least 1")
		}
		if data.IsError == nil {
			return nil, fmt.Errorf("isError is required")
		}
		if data.Truncated == nil {
			return nil, fmt.Errorf("truncated is required")
		}
		return data, nil
	case eventv1alpha1.TypeError:
		var data errorEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, fmt.Errorf("decode error data: %w", err)
		}
		if strings.TrimSpace(data.Code) == "" {
			return nil, fmt.Errorf("code is required and must not be empty")
		}
		if strings.TrimSpace(data.Message) == "" {
			return nil, fmt.Errorf("message is required and must not be empty")
		}
		return data, nil
	case eventv1alpha1.TypeOutput:
		var data outputEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, fmt.Errorf("decode output data: %w", err)
		}
		if strings.TrimSpace(data.MediaType) == "" {
			return nil, fmt.Errorf("mediaType is required and must not be empty")
		}
		if data.Value == nil {
			return nil, fmt.Errorf("value is required")
		}
		return data, nil
	case eventv1alpha1.TypeUsage:
		var data usageEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, fmt.Errorf("decode usage data: %w", err)
		}
		if data.Turn == nil {
			return nil, fmt.Errorf("turn is required")
		}
		if *data.Turn < 1 {
			return nil, fmt.Errorf("turn must be at least 1")
		}
		if data.Usage == nil {
			return nil, fmt.Errorf("usage is required and must be an object")
		}
		return data, nil
	default:
		return nil, fmt.Errorf("unsupported event type %q", event.Type)
	}
}

func summarizeEvent(summary *EventSummary, event eventv1alpha1.Event, parsedData any) {
	metadata := EventMetadata{Timestamp: event.Timestamp, Type: event.Type}
	switch data := parsedData.(type) {
	case lifecycleEventData:
		metadata.Phase = data.Phase
		summary.Lifecycle = append(summary.Lifecycle, data.Phase)
	case toolEventData:
		tool := ToolEvent{
			Name:      data.Name,
			Count:     *data.Count,
			IsError:   *data.IsError,
			ErrorCode: data.ErrorCode,
			Truncated: *data.Truncated,
		}
		summary.Tools = append(summary.Tools, tool)
		metadata.Name = tool.Name
		metadata.IsError = tool.IsError
		metadata.ErrorCode = tool.ErrorCode
		metadata.Truncated = tool.Truncated
	case errorEventData:
		metadata.ErrorCode = data.Code
		summary.Errors = append(summary.Errors, data.Code)
	case outputEventData, usageEventData:
	}
	summary.Metadata = append(summary.Metadata, metadata)
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
