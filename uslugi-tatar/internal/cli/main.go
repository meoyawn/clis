package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata"
)

const portalURL = "https://uslugi.tatarstan.ru"

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(errOut, `Usage:
  uslugi-tatar auth import --browser <browser> [--profile <profile>]
  uslugi-tatar auth status
  uslugi-tatar accounts [--yaml] [--account <number>]
  uslugi-tatar {gas|main} [--dry-run] meters.yaml`)
		if len(args) > 0 {
			return nil
		}
		return fmt.Errorf("expected auth, accounts, gas or main")
	}
	if args[0] == "auth" {
		return runAuth(ctx, args[1:], out, errOut)
	}
	if args[0] == "accounts" {
		return runAccounts(ctx, args[1:], out, errOut)
	}
	if args[0] != "gas" && args[0] != "main" {
		return fmt.Errorf("unknown command %q; expected auth, accounts, gas or main", args[0])
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	dryRun := flags.Bool("dry-run", false, "show account, meters and usage without submitting readings")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("%s requires one meters.yaml path; put flags before the path", args[0])
	}
	zone, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return fmt.Errorf("load Moscow timezone: %w", err)
	}
	now := time.Now().In(zone)
	var gas gasReading
	var main mainReading
	if args[0] == "gas" {
		gas, err = loadGasReading(flags.Arg(0), now)
	} else {
		main, err = loadMainReading(flags.Arg(0), now)
	}
	if err != nil {
		return err
	}
	client, err := authenticatedClient()
	if err != nil {
		return err
	}
	if args[0] == "gas" {
		return client.gas(ctx, gas, *dryRun, out)
	}
	return client.submitMain(ctx, main, *dryRun, out)
}

// Main runs the command-line application.
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "uslugi-tatar:", err)
		os.Exit(1)
	}
}
