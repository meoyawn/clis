package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
)

// Main runs the command-line application.
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "linkedin:", err)
		os.Exit(1)
	}
}
