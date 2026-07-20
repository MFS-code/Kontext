package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func execute(
	args []string,
	getenv func(string) string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	destination, install, err := installDestination(args)
	if err != nil {
		fmt.Fprintf(stderr, "kontext reporter: %v\n", err)
		return reporterFailureExitCode
	}
	if install {
		if err := installCurrentExecutable(destination); err != nil {
			fmt.Fprintf(stderr, "kontext reporter: %v\n", err)
			return reporterFailureExitCode
		}
		return 0
	}

	config, err := parseConfig(args, getenv)
	if err != nil {
		fmt.Fprintf(
			stderr,
			"kontext reporter: %v\nusage: kontext-reporter [--format last-line|kontext-envelope] [--termination-log path] -- command [args...]\n",
			err,
		)
		return reporterFailureExitCode
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)

	return runReporter(config, stdout, stderr, signals)
}
