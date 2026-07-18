package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	MaxJudgeInputBytes  = 64 << 10
	MaxJudgeOutputBytes = 16 << 10
	MaxJudgeRationale   = 4096
)

type Judge interface {
	Evaluate(context.Context, JudgeObservation) (JudgeResult, error)
}

type JudgeObservation struct {
	Suite         string               `json:"suite"`
	CaseID        string               `json:"caseId"`
	Description   string               `json:"description,omitempty"`
	TerminalPhase string               `json:"terminalPhase,omitempty"`
	StatusResult  string               `json:"statusResult,omitempty"`
	StatusOutput  *StatusOutput        `json:"statusOutput,omitempty"`
	Envelope      *EnvelopeObservation `json:"envelope,omitempty"`
	EventCounts   map[string]int       `json:"eventCounts,omitempty"`
	Grades        []Grade              `json:"deterministicGrades"`
}

type CommandJudge struct {
	Command string
	Timeout time.Duration
}

func (judge CommandJudge) Evaluate(ctx context.Context, observation JudgeObservation) (JudgeResult, error) {
	if strings.TrimSpace(judge.Command) == "" {
		return JudgeResult{}, errors.New("judge command is empty")
	}
	input, err := json.Marshal(observation)
	if err != nil {
		return JudgeResult{}, err
	}
	if len(input) > MaxJudgeInputBytes {
		return JudgeResult{}, fmt.Errorf("judge observation exceeds %d bytes", MaxJudgeInputBytes)
	}
	timeout := judge.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, "/bin/sh", "-c", judge.Command)
	hardenJudgeCommand(command)
	command.Stdin = bytes.NewReader(input)
	command.Env = safeCommandEnvironment()
	var stdout bytes.Buffer
	command.Stdout = &limitedWriter{Writer: &stdout, Remaining: MaxJudgeOutputBytes}
	var stderr bytes.Buffer
	command.Stderr = &limitedWriter{Writer: &stderr, Remaining: MaxJudgeOutputBytes}
	runErr := command.Run()
	terminateJudgeCommand(command)
	if runErr != nil {
		return JudgeResult{}, fmt.Errorf("judge command: %w: %s", runErr, boundedMessage(stderr.String()))
	}
	var response struct {
		Pass      *bool    `json:"pass"`
		Score     *float64 `json:"score"`
		Rationale *string  `json:"rationale"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return JudgeResult{}, fmt.Errorf("decode judge response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return JudgeResult{}, errors.New("decode judge response: trailing data")
	}
	if response.Pass == nil || response.Score == nil || response.Rationale == nil {
		return JudgeResult{}, errors.New("judge response requires pass, score, and rationale")
	}
	if *response.Score < 0 || *response.Score > 1 {
		return JudgeResult{}, errors.New("judge score must be between 0 and 1")
	}
	result := JudgeResult{
		Configured: true,
		Pass:       *response.Pass,
		Score:      *response.Score,
		Rationale:  *response.Rationale,
	}
	if len(result.Rationale) > MaxJudgeRationale {
		result.Rationale = result.Rationale[:MaxJudgeRationale]
	}
	return result, nil
}

func observationFor(record Record) JudgeObservation {
	observation := JudgeObservation{
		Suite:         record.Suite,
		CaseID:        record.CaseID,
		Description:   record.Description,
		TerminalPhase: string(record.TerminalPhase),
		StatusResult:  boundedMessage(record.StatusResult),
		StatusOutput:  record.StatusOutput,
		Grades:        record.Grades,
		EventCounts:   make(map[string]int, len(record.Events.Counts)),
	}
	if observation.StatusOutput != nil && len(observation.StatusOutput.Value) > 16<<10 {
		observation.StatusOutput = &StatusOutput{
			MediaType: observation.StatusOutput.MediaType,
			Value:     json.RawMessage(`null`),
		}
	}
	observation.Envelope = record.Envelope
	for eventType, count := range record.Events.Counts {
		observation.EventCounts[string(eventType)] = count
	}
	return observation
}

func safeCommandEnvironment() []string {
	var result []string
	for _, key := range []string{"PATH", "LANG", "LC_ALL"} {
		if value := os.Getenv(key); value != "" {
			result = append(result, key+"="+value)
		}
	}
	return result
}

type limitedWriter struct {
	Writer    io.Writer
	Remaining int
}

func (writer *limitedWriter) Write(data []byte) (int, error) {
	original := len(data)
	if writer.Remaining <= 0 {
		return original, nil
	}
	if len(data) > writer.Remaining {
		data = data[:writer.Remaining]
	}
	if _, err := writer.Writer.Write(data); err != nil {
		return 0, err
	}
	writer.Remaining -= len(data)
	return original, nil
}
