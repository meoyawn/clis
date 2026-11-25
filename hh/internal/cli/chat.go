package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/meoyawn/clis/hh/internal/domain"
	hh "github.com/meoyawn/clis/hh/internal/service"
)

// runChat reads or replies in one explicitly selected chat at chatik.hh.ru.
func runChat(ctx context.Context, args []string, profile, cookies string, headers http.Header) error {
	if len(args) < 2 || args[0] != "read" && args[0] != "reply" {
		return fmt.Errorf("chat requires read CHAT or reply CHAT --text-file FILE; run hh --help")
	}
	id, err := domain.NewChatIDFromValue(args[1])
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("chat "+args[0], flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	output := flags.String("output", "", "JSON output file")
	var textFile, approved, backupDir string
	var send bool
	if args[0] == "reply" {
		flags.StringVar(&textFile, "text-file", "", "complete reply UTF-8 file")
		flags.BoolVar(&send, "send", false, "send only after human approval of the exact preview")
		flags.StringVar(&approved, "approved", "", "approved preview SHA-256")
		flags.StringVar(&backupDir, "backup-dir", filepath.Join(stateDir(), "backups"), "private pre-send backup directory")
	}
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected chat %s arguments %q", args[0], flags.Args())
	}
	var text []byte
	if args[0] == "reply" {
		if textFile == "" {
			return fmt.Errorf("chat reply requires --text-file FILE")
		}
		if send && len(approved) != 64 || !send && approved != "" {
			return fmt.Errorf("sending requires both --send and --approved SHA256 after human approval of the exact reply")
		}
		if text, err = os.ReadFile(textFile); err != nil {
			return fmt.Errorf("read reply text %q: %w", textFile, err)
		}
	}
	unlock, err := lockAccount()
	if err != nil {
		return err
	}
	defer unlock()
	base, _ := url.Parse("https://chatik.hh.ru")
	client, err := hh.BrowserClient(ctx, base, profile, cookies, headers)
	if err != nil {
		return err
	}
	if args[0] == "read" {
		page, err := client.ReadChat(ctx, id)
		if err != nil {
			return err
		}
		return outputJSON(*output, page)
	}
	result, err := client.ReplyChat(ctx, id, string(text), send, approved, backupDir)
	if err != nil {
		return err
	}
	return outputJSON(*output, result)
}
