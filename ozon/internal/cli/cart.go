package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
)

func runCart(ctx context.Context, args []string, stdout, stderr io.Writer, setup setupFunc) error {
	if len(args) == 0 {
		return fmt.Errorf("cart expects add, remove or list; %s", cartUsage)
	}
	command := args[0]
	switch command {
	case "help", "--help", "-h":
		_, err := io.WriteString(stdout, cartUsage)
		return err
	case "add", "remove", "list":
	default:
		return fmt.Errorf("unknown cart command %q; use add, remove or list", command)
	}
	flags := flag.NewFlagSet("cart "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var usageErr error
	flags.Usage = func() { usageErr = printCommandUsage(flags) }
	timeout := flags.Duration("timeout", 60*time.Second, "whole command timeout")
	ua := flags.String("user-agent", "", "HTTP User-Agent")
	if err := parseOptions(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usageErr
		}
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout %s must be positive", timeout.String())
	}
	if strings.ContainsAny(*ua, "\r\n") {
		return fmt.Errorf("user-agent must not contain newlines")
	}
	var ref ozon.ItemReference
	if command == "list" {
		if flags.NArg() != 0 {
			return fmt.Errorf("cart list does not accept positional arguments")
		}
	} else {
		if flags.NArg() != 1 {
			return fmt.Errorf("cart %s expects one SKU or URL, got %d arguments", command, flags.NArg())
		}
		var err error
		ref, err = ozon.ParseItemReference(flags.Arg(0))
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
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	opt := ozon.RequestOptions{UserAgent: *ua}
	var result any
	switch command {
	case "list":
		result, err = deps.client.CartList(ctx, opt)
	case "add":
		result, err = deps.client.CartAdd(ctx, ref, opt)
	case "remove":
		result, err = deps.client.CartRemove(ctx, ref, opt)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}
