package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Main runs the command-line application.
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		reportError(err, os.Stderr)
		os.Exit(1)
	}
}
