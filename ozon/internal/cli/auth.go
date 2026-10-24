package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
)

func runAuth(ctx context.Context, args []string, stdout, stderr io.Writer, setup setupFunc) error {
	if len(args) == 0 {
		return fmt.Errorf("auth expects import or status; %s", authUsage)
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := io.WriteString(stdout, authUsage)
		return err
	}
	if args[0] != "import" && args[0] != "status" {
		return fmt.Errorf("unknown auth command %q; use import or status", args[0])
	}
	flags := flag.NewFlagSet("auth "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var usageErr error
	flags.Usage = func() { usageErr = printCommandUsage(flags) }
	var browserValue, profile string
	timeout := flags.Duration("timeout", 60*time.Second, "whole command timeout")
	userAgent := flags.String("user-agent", "", "HTTP User-Agent override")
	jsonOutput := flags.Bool("json", false, "print authentication details as JSON")
	if args[0] == "import" {
		flags.StringVar(&browserValue, "browser", "", "chrome, firefox, brave, edge or safari (required)")
		flags.StringVar(&profile, "browser-profile", "", "browser profile name or directory")
	}
	if err := parseOptions(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usageErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("auth %s does not accept positional arguments", args[0])
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout %q must be positive", timeout.String())
	}
	if strings.ContainsAny(*userAgent, "\r\n") {
		return fmt.Errorf("user-agent must not contain newlines")
	}
	var browser ozon.Browser
	if args[0] == "import" {
		if browserValue == "" {
			return fmt.Errorf("auth import requires --browser, for example --browser chrome")
		}
		var err error
		browser, err = ozon.NewBrowserFromValue(browserValue)
		if err != nil {
			return err
		}
	}
	deps, err := setup()
	if err != nil {
		return err
	}
	if deps.close != nil {
		defer deps.close()
	}
	var status *ozon.AuthStatus
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	options := ozon.RequestOptions{UserAgent: *userAgent}
	if args[0] == "status" {
		status, err = deps.client.AuthStatus(ctx, options)
	} else {
		status, err = deps.client.ImportAuth(ctx, browser, profile, deps.readCookies)
	}
	if *jsonOutput && status != nil {
		if outputErr := writeJSON(stdout, status); outputErr != nil {
			return outputErr
		}
		return err
	}
	if err != nil {
		return err
	}
	message := "Signed in to Ozon."
	if status.Account != nil && terminalText(status.Account.Name) != "" {
		message = "Signed in as " + terminalText(status.Account.Name) + "."
	}
	if args[0] == "import" {
		message = fmt.Sprintf("Imported %d Ozon cookies. Session saved.", status.CookieCount)
	}
	_, err = fmt.Fprintln(stdout, message)
	return err
}

func terminalText(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}), " ")
}

// Flags can appear on either side of positionals. A literal --
// ends option parsing, so searches beginning with '-' remain expressible.
func parseOptions(flags *flag.FlagSet, args []string) error {
	var options, positional []string
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
		options = append(options, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := flags.Lookup(name)
		if option != nil && !hasValue {
			boolFlag, isBool := option.Value.(interface{ IsBoolFlag() bool })
			if !isBool || !boolFlag.IsBoolFlag() {
				if i+1 >= len(args) {
					return fmt.Errorf("flag %s requires a value", arg)
				}
				options = append(options, args[i+1]) // #nosec G602 -- The preceding check ensures i+1 is within args.
				i++
			}
		}
	}
	return flags.Parse(append(append(options, "--"), positional...))
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}
