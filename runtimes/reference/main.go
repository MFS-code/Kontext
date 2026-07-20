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

	resultv1alpha1 "github.com/MFS-code/Kontext/pkg/result/v1alpha1"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/config"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/engine"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/events"
	"github.com/MFS-code/Kontext/runtimes/reference/internal/tools"
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
		if emitErr := engine.EmitFailure(
			stdout,
			emitter,
			"invalid_configuration",
			err.Error(),
			nil,
			engine.Metadata{
				Provider:    strings.TrimSpace(getenv("KONTEXT_PROVIDER")),
				Model:       getenv("KONTEXT_MODEL"),
				StartedAt:   startedAt,
				CompletedAt: now().UTC(),
			},
		); emitErr != nil {
			fmt.Fprintf(stderr, "kontext reference runtime: emit result: %v\n", emitErr)
		}
		return 2
	}

	execution := engine.Runner{
		Emitter: emitter,
		Now:     now,
		ResolveToolsContext: func(ctx context.Context, runtimeConfig config.Config) (engine.ToolExecutor, error) {
			return tools.NewWithContext(ctx, tools.Config{
				Allowed: runtimeConfig.Tools,
				Stdout:  stdout,
				Stderr:  stderr,
				MCP:     runtimeConfig.MCP,
			})
		},
	}.Run(ctx, runtimeConfig)
	if err := resultv1alpha1.WriteEnvelopeLine(stdout, execution.Envelope); err != nil {
		fmt.Fprintf(stderr, "kontext reference runtime: emit result: %v\n", err)
		return 1
	}
	return execution.ExitCode
}
