package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func WriteOutputs(recordPath, summaryPath string, records []Record, summary Summary) error {
	if err := writeJSONLAtomic(recordPath, records); err != nil {
		return err
	}
	summary.RecordPath = recordPath
	if err := writeJSONAtomic(summaryPath, summary); err != nil {
		return err
	}
	return nil
}

func BuildSummary(suite string, startedAt, completedAt time.Time, records []Record, recordPath string) Summary {
	summary := Summary{
		APIVersion:  APIVersion,
		Suite:       suite,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Total:       len(records),
		RecordPath:  recordPath,
	}
	for _, record := range records {
		if record.Pass {
			summary.Passed++
		} else {
			summary.Failed++
		}
	}
	return summary
}

func writeJSONLAtomic(path string, records []Record) error {
	return atomicWrite(path, func(file *os.File) error {
		writer := bufio.NewWriter(file)
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		for _, record := range records {
			if err := encoder.Encode(record); err != nil {
				return err
			}
		}
		return writer.Flush()
	})
}

func writeJSONAtomic(path string, value any) error {
	return atomicWrite(path, func(file *os.File) error {
		encoder := json.NewEncoder(file)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	})
}

func atomicWrite(path string, write func(*os.File) error) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.CreateTemp(parent, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	cleanup := func() {
		file.Close()
		os.Remove(tempPath)
	}
	if err := write(file); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}
