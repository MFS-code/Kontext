package eval

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	labelManagedBy  = "app.kubernetes.io/managed-by"
	labelEvalSuite  = "kontext.dev/eval-suite"
	labelEvalCase   = "kontext.dev/eval-case"
	labelInvocation = "kontext.dev/eval-invocation"
)

type RunnerOptions struct {
	Namespace    string
	KeepRuns     bool
	PollInterval time.Duration
	Judge        Judge
	Now          func() time.Time
	InvocationID string
}

type Runner struct {
	Client  client.Client
	Logs    LogFetcher
	Options RunnerOptions
}

func normalizeRunnerOptions(options RunnerOptions, defaults SuiteDefaults) RunnerOptions {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.InvocationID == "" {
		options.InvocationID = invocationID(options.Now())
	}
	if options.Namespace == "" {
		options.Namespace = defaults.Namespace
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 500 * time.Millisecond
	}
	return options
}

func invocationID(now time.Time) string {
	random := make([]byte, 3)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%x", now.UnixNano())
	}
	return fmt.Sprintf("%x-%s", now.Unix(), hex.EncodeToString(random))
}

func labelValue(value string) string {
	value = NameForCase(value, "", "")
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}
