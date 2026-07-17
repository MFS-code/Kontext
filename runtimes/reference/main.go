package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/config"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/engine"
	"github.com/kontext-dev/kontext/runtimes/reference/internal/events"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	os.Exit(run(ctx, os.Getenv, os.Stdout, os.Stderr, time.Now))
}

func run(
	ctx context.Context,
	getenv func(string) string,
	stdout io.Writer,
	stderr io.Writer,
	now func() time.Time,
) int {
	emitter := events.NewEmitter(stdout, stderr, now)
	runtimeConfig, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "kontext reference runtime: %v\n", err)
		startedAt := now().UTC()
		emitter.Emit(events.TypeError, map[string]any{
			"code":    "invalid_configuration",
			"message": err.Error(),
		})
		envelope := engine.Failure(
			"invalid_configuration",
			err.Error(),
			nil,
			engine.Metadata{
				Provider:    strings.TrimSpace(getenv("KONTEXT_PROVIDER")),
				Model:       strings.TrimSpace(getenv("KONTEXT_MODEL")),
				StartedAt:   startedAt,
				CompletedAt: now().UTC(),
			},
		)
		if emitErr := resultv1alpha1.WriteEnvelopeLine(stdout, envelope); emitErr != nil {
			fmt.Fprintf(stderr, "kontext reference runtime: emit result: %v\n", emitErr)
		}
		return 2
	}

	execution := engine.Runner{
		Emitter: emitter,
		Now:     now,
	}.Run(ctx, runtimeConfig)
	if err := resultv1alpha1.WriteEnvelopeLine(stdout, execution.Envelope); err != nil {
		fmt.Fprintf(stderr, "kontext reference runtime: emit result: %v\n", err)
		return 1
	}
	return execution.ExitCode
}
