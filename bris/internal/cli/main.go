package cli

import (
	"context"
	"encoding/json"
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

type credentials struct {
	Login    string       `json:"login"`
	Password string       `json:"password"`
	Account  string       `json:"account"`
	Meters   meterNumbers `json:"meters"`
}

type meterNumbers struct {
	Water    string `json:"water"`
	Day      string `json:"day"`
	Night    string `json:"night"`
	Drinking string `json:"drinking"`
}

func readCredentials(path string) (credentials, error) {
	var result credentials
	f, err := os.Open(path)
	if err != nil {
		return result, fmt.Errorf("open credentials %q: %w (see credentials.example.json)", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return result, fmt.Errorf("stat credentials %q: %w", path, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return result, fmt.Errorf("credentials %q must be private; run chmod 600 on this file", path)
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&result); err != nil {
		// Decoder errors can contain credential values, so do not print them.
		return result, fmt.Errorf("credentials %q must contain a valid configuration object; see credentials.example.json", path)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return result, fmt.Errorf("credentials %q must contain exactly one JSON object", path)
	}
	if result.Login == "" || result.Password == "" {
		return result, fmt.Errorf("credentials %q require nonempty login and password", path)
	}
	return result, nil
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("bris", flag.ContinueOnError)
	flags.SetOutput(errOut)
	dryRun := flags.Bool("dry-run", false, "log in and compare readings without submitting them")
	credentialsPath := flags.String("credentials", "credentials.json", "private JSON file containing login, password, account and meter serials")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: bris [--dry-run] [--credentials credentials.json] meters.yaml")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return fmt.Errorf("expected one meters.yaml path, got %d positional arguments; put flags before the path", flags.NArg())
	}
	zone, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return fmt.Errorf("load Moscow timezone: %w", err)
	}
	now := time.Now().In(zone)
	reading, err := loadReading(flags.Arg(0), now)
	if err != nil {
		return err
	}
	creds, err := readCredentials(*credentialsPath)
	if err != nil {
		return err
	}
	client, err := newBrisClient("https://lk.bris-cloud.ru", creds)
	if err != nil {
		return err
	}
	return client.submit(ctx, reading, now, *dryRun, out)
}

// Main runs the command-line application.
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "bris:", err)
		os.Exit(1)
	}
}
