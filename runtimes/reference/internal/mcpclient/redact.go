package mcpclient

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/MFS-code/Kontext/internal/tooloutput"
)

const (
	maxExternalErrorBytes = int64(4 << 10)
	maxStderrLineBytes    = 64 << 10
)

type redactor struct {
	values         []string
	sensitiveForms []string
}

func newRedactor(values []string) redactor {
	cloned := append([]string(nil), values...)
	sort.Slice(cloned, func(left int, right int) bool {
		return len(cloned[left]) > len(cloned[right])
	})
	forms := make([]string, 0, len(cloned)*2)
	for _, sensitive := range cloned {
		if sensitive == "" {
			continue
		}
		forms = append(forms, sensitive)
		encoded, _ := json.Marshal(sensitive)
		escaped := strings.Trim(string(encoded), `"`)
		if escaped != sensitive {
			forms = append(forms, escaped)
		}
	}
	return redactor{values: cloned, sensitiveForms: forms}
}

func (redactor redactor) clean(value string) string {
	return tooloutput.TruncateUTF8(redactor.replace(value), maxExternalErrorBytes)
}

func (redactor redactor) replace(value string) string {
	for _, sensitive := range redactor.values {
		if sensitive != "" {
			value = strings.ReplaceAll(value, sensitive, "[REDACTED]")
		}
	}
	return value
}

func (redactor redactor) containsSensitive(value string) bool {
	for _, sensitive := range redactor.sensitiveForms {
		if strings.Contains(value, sensitive) {
			return true
		}
	}
	return false
}

func redactStructuredValue(value any, redactor redactor) any {
	switch typed := value.(type) {
	case string:
		return redactor.replace(typed)
	case []any:
		for index := range typed {
			typed[index] = redactStructuredValue(typed[index], redactor)
		}
		return typed
	case map[string]any:
		for key, child := range typed {
			typed[key] = redactStructuredValue(child, redactor)
		}
		return typed
	default:
		return value
	}
}

func (current *server) safeMessage(value string) string {
	return current.redactor.clean(value)
}

func (current *server) safeError(code string, prefix string, err error) *Error {
	message := prefix
	if err != nil {
		message += ": " + err.Error()
	}
	return &Error{Code: code, Message: current.safeMessage(message)}
}

type redactingLineWriter struct {
	mu         sync.Mutex
	sink       io.Writer
	redactor   redactor
	buffer     []byte
	discarding bool
}

func newRedactingLineWriter(sink io.Writer, redactor redactor) io.Writer {
	return &redactingLineWriter{sink: sink, redactor: redactor}
}

func (writer *redactingLineWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	originalLength := len(data)
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		var part []byte
		if newline < 0 {
			part = data
			data = nil
		} else {
			part = data[:newline+1]
			data = data[newline+1:]
		}
		if writer.discarding {
			if newline >= 0 {
				writer.discarding = false
			}
			continue
		}
		if len(writer.buffer)+len(part) > maxStderrLineBytes {
			writer.buffer = writer.buffer[:0]
			writer.discarding = newline < 0
			_, _ = io.WriteString(writer.sink, "MCP stderr line omitted: too long\n")
			continue
		}
		writer.buffer = append(writer.buffer, part...)
		if newline >= 0 {
			_, _ = io.WriteString(writer.sink, writer.redactor.clean(string(writer.buffer)))
			writer.buffer = writer.buffer[:0]
		}
	}
	return originalLength, nil
}
