package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/meoyawn/clis/ozon/internal/webview"
)

// Main runs the command-line application. The system web view's loop starts
// on the main goroutine only if a command needs to renew Ozon's anti-bot cookie.
func Main() {
	os.Exit(webview.Run(runMain))
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
