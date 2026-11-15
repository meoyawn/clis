package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/meoyawn/clis/tastycoffee/internal/service"
)

const usage = `tastycoffee — Tasty Coffee account and orders; JSON output

Usage:
  tastycoffee auth import [--browser chrome] [--browser-profile Default]
  tastycoffee auth status [--check]
  tastycoffee account
  tastycoffee orders [--page N] [--limit N] [--current]
  tastycoffee latest

Options:
  --session-file PATH   Override the local owner-only session file
  --timeout 60s         Whole-command timeout
  --json                Accepted; output is always JSON

auth import reads only auth.token for shop.tastycoffee.ru from Chrome using
sweetcookie and the OS keychain. It saves the session outside the repository
with mode 0600. --browser-profile accepts a Chrome profile name or path.
Only explicit import reads Chrome. import/status normally make no HTTP calls.
auth status --check makes one authenticated request and includes account_id
on success; local session availability alone does not establish server validity.

account reports the current personal discount percentage. orders returns one
server page of historical orders (default page 1, limit 10; limit 1–100).
--current selects active orders instead. latest makes two requests and returns
the newest order across both lists.
All prices are rubles. Product prices are historical prices before order-level
discounts; prices contains the actual discount and delivery lines. Option IDs
are product option value IDs. Bulky catalog descriptions and buyer contacts
are omitted. Historical products may contain today's catalog prices upstream;
those catalog prices are deliberately excluded.

Expired/rejected sessions require another explicit auth import. Requests never
follow redirects or retry. No command creates, cancels, or changes orders.
`

func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tastycoffee:", err)
		os.Exit(1)
	}
}

func Run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	command := args[0]
	if command != "auth" && command != "account" && command != "orders" && command != "latest" {
		return fmt.Errorf("unknown command %q; use --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("session-file", "", "local session file")
	timeout := fs.Duration("timeout", 60*time.Second, "whole-command timeout")
	fs.Bool("json", false, "JSON output")
	var profile, browser string
	var check, current bool
	page, limit := 1, 10
	if command == "auth" {
		fs.StringVar(&profile, "browser-profile", "Default", "Chrome profile name or path")
		fs.StringVar(&browser, "browser", "chrome", "Chrome only")
		fs.BoolVar(&check, "check", false, "verify saved session with Tasty Coffee")
	}
	if command == "orders" {
		fs.IntVar(&page, "page", 1, "server page")
		fs.IntVar(&limit, "limit", 10, "server page size")
		fs.BoolVar(&current, "current", false, "active orders only")
	}
	if err := parseOptions(fs, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(stdout, usage)
		}
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("invalid timeout %s; must be positive", *timeout)
	}
	if command == "auth" {
		if fs.NArg() != 1 || (fs.Arg(0) != "status" && fs.Arg(0) != "import") {
			return fmt.Errorf("auth requires import or status")
		}
		if browser != "chrome" {
			return fmt.Errorf("unsupported browser %q; use chrome", browser)
		}
		if check && fs.Arg(0) != "status" {
			return fmt.Errorf("--check requires auth status")
		}
	} else if fs.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", command)
	}
	if command == "orders" && (page < 1 || limit < 1 || limit > 100) {
		return fmt.Errorf("invalid page %d or limit %d; page must be positive and limit must be 1 through 100", page, limit)
	}
	if *path == "" {
		p, err := service.SessionPath()
		if err != nil {
			return err
		}
		*path = p
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if command == "auth" && fs.Arg(0) == "import" {
		status, err := service.ImportSession(ctx, *path, profile)
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	}
	session, err := service.LoadSession(*path)
	if err != nil {
		return err
	}
	status := service.Status(*path, session)
	if command == "auth" && !check {
		return writeJSON(stdout, status)
	}
	if !status.SessionAvailable {
		return fmt.Errorf("saved session is missing or expired; sign in to %s in Chrome, then run auth import", service.Origin)
	}
	httpClient := &http.Client{Timeout: *timeout, Transport: http.DefaultTransport.(*http.Transport).Clone()}
	defer httpClient.CloseIdleConnections()
	client, err := service.NewClient(httpClient, session)
	if err != nil {
		return err
	}
	var result any
	switch command {
	case "auth":
		account, accountErr := client.Account(ctx)
		if accountErr != nil {
			return accountErr
		}
		status.AccountID = &account.ID
		result = status
	case "account":
		result, err = client.Account(ctx)
	case "orders":
		result, err = client.Orders(ctx, page, limit, current)
	case "latest":
		result, err = client.Latest(ctx)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func parseOptions(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if f := fs.Lookup(name); f != nil && !hasValue {
			boolFlag, ok := f.Value.(interface{ IsBoolFlag() bool })
			if !ok || !boolFlag.IsBoolFlag() {
				if i+1 == len(args) {
					return fmt.Errorf("flag %s requires a value", arg)
				}
				i++
				flags = append(flags, args[i])
			}
		}
	}
	flags = append(flags, "--")
	return fs.Parse(append(flags, positional...))
}
