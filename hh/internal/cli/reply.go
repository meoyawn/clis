package cli

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

func runApplicationReply(ctx context.Context, args []string, baseText, profile, cookies string) error {
	if len(args) == 0 {
		return fmt.Errorf("application reply requires VACANCY --resume HASH --text-file FILE")
	}
	vacancy, err := hh.NewVacancyIDFromValue(args[0])
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("application reply", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	resumeText := flags.String("resume", "", "exact application resume hash")
	textFile := flags.String("text-file", "", "complete reply UTF-8 file")
	output := flags.String("output", "", "JSON output file")
	send := flags.Bool("send", false, "send only after human approval of the exact preview")
	approved := flags.String("approved", "", "approved preview SHA-256")
	backupDir := flags.String("backup-dir", filepath.Join(stateDir(), "backups"), "private pre-send backup directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected reply arguments %q", flags.Args())
	}
	resume, err := hh.NewResumeIDFromValue(*resumeText)
	if err != nil {
		return err
	}
	if *textFile == "" {
		return fmt.Errorf("application reply requires --text-file FILE")
	}
	if *send && len(*approved) != 64 || !*send && *approved != "" {
		return fmt.Errorf("sending requires both --send and --approved SHA256 after human approval of the exact reply")
	}
	text, err := os.ReadFile(*textFile)
	if err != nil {
		return fmt.Errorf("read reply text %q: %w", *textFile, err)
	}
	base, err := url.Parse(baseText)
	if err != nil {
		return fmt.Errorf("invalid base URL %q: %w", baseText, err)
	}
	if base.Path != "" && base.Path != "/" || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("invalid base URL %q: use only an origin", baseText)
	}
	base.Path = ""
	unlock, err := lockAccount()
	if err != nil {
		return err
	}
	defer unlock()
	httpClient, err := hh.BrowserClient(ctx, base, profile, cookies)
	if err != nil {
		return err
	}
	status, err := hh.NewClient(httpClient, base).Application(ctx, vacancy, resume)
	if err != nil {
		return err
	}
	chatBase, _ := url.Parse("https://chatik.hh.ru")
	chatHTTP, err := hh.BrowserClient(ctx, chatBase, profile, cookies)
	if err != nil {
		return err
	}
	result, err := hh.NewClient(chatHTTP, chatBase).ReplyApplicationMessage(ctx, status, string(text), *send, *approved, *backupDir)
	if err != nil {
		return err
	}
	return outputJSON(*output, result)
}
