package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
)

// Main runs the command-line application.
func Main() {
	os.Exit(runMain())
}

func runMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "ozon:", err)
		return 1
	}
	return 0
}
