package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
)

const (
	defaultTerminationPath = "/dev/termination-log"
	defaultCaptureBytes    = 64 * 1024
)

type CaptureFormat string

const (
	CaptureFormatLastLine        CaptureFormat = "last-line"
	CaptureFormatKontextEnvelope CaptureFormat = "kontext-envelope"
)

type Config struct {
	Format          CaptureFormat
	TerminationPath string
	MaxCaptureBytes int
	Command         []string
}

func parseConfig(args []string, getenv func(string) string) (Config, error) {
	formatDefault := getenv("KONTEXT_RESULT_FORMAT")
	if formatDefault == "" {
		formatDefault = string(CaptureFormatLastLine)
	}
	terminationDefault := getenv("KONTEXT_TERMINATION_MESSAGE")
	if terminationDefault == "" {
		terminationDefault = defaultTerminationPath
	}

	flags := flag.NewFlagSet("kontext-reporter", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	formatValue := flags.String("format", formatDefault, "result format: last-line or kontext-envelope")
	terminationPath := flags.String("termination-log", terminationDefault, "termination message path")
	maxCaptureBytes := flags.Int("max-capture-bytes", defaultCaptureBytes, "maximum bytes retained from one result line")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}

	format, err := parseCaptureFormat(*formatValue)
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(*terminationPath) == "" {
		return Config{}, errors.New("termination log path cannot be empty")
	}
	if *maxCaptureBytes < resultv1alpha1.MaxTerminationMessageBytes {
		return Config{}, fmt.Errorf(
			"max capture bytes must be at least %d",
			resultv1alpha1.MaxTerminationMessageBytes,
		)
	}
	command := flags.Args()
	if len(command) == 0 {
		return Config{}, errors.New("child command is required after --")
	}

	return Config{
		Format:          format,
		TerminationPath: *terminationPath,
		MaxCaptureBytes: *maxCaptureBytes,
		Command:         command,
	}, nil
}

func parseCaptureFormat(value string) (CaptureFormat, error) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	switch normalized {
	case "lastline", string(CaptureFormatLastLine):
		return CaptureFormatLastLine, nil
	case "kontextenvelope", string(CaptureFormatKontextEnvelope):
		return CaptureFormatKontextEnvelope, nil
	default:
		return "", fmt.Errorf("unsupported result format %q", value)
	}
}
