package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/kontext-dev/kontext/internal/eval"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(eval.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
