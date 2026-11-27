package cli

import (
	"context"
	"os/exec"
	"runtime"
	"time"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
)

var macBrowserApps = map[string]string{
	"chrome": "Google Chrome", "brave": "Brave Browser", "edge": "Microsoft Edge",
	"firefox": "Firefox", "safari": "Safari",
}

// openInBrowser loads target in a background tab of the imported browser so
// it can pass Ozon's anti-bot check with the user's own profile.
func openInBrowser(ctx context.Context, browser ozon.Browser, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", "-g", "-a", macBrowserApps[browser.String()], target)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", target)
	}
	return cmd.Run()
}
