package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
)

// Main runs the command-line application.
func Main() {
	if err := runMain(); err != nil {
		fmt.Fprintln(os.Stderr, "avito:", err)
		os.Exit(1)
	}
}

func runMain() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
}
