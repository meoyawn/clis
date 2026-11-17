package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	_ "time/tzdata"
)

type credentials struct {
	Login    string       `yaml:"login"`
	Password string       `yaml:"password"`
	Account  string       `yaml:"account"`
	Meters   meterNumbers `yaml:"meters"`
}

type meterNumbers struct {
	Water    string `yaml:"water"`
	Day      string `yaml:"day"`
	Night    string `yaml:"night"`
	Drinking string `yaml:"drinking"`
}

func (value *credentials) UnmarshalYAML(node *yaml.Node) error {
	// Decode this block separately so errors cannot expose credential values or keys.
	type config credentials
	invalid := fmt.Errorf("invalid bris configuration; see --help for login, password, account and meters")
	data, err := yaml.Marshal(node)
	if err != nil {
		return invalid
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode((*config)(value)); err != nil {
		return invalid
	}
	return nil
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("bris", flag.ContinueOnError)
	flags.SetOutput(errOut)
	dryRun := flags.Bool("dry-run", false, "log in and compare readings without submitting them")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: bris [--dry-run] meters.yaml")
		flags.PrintDefaults()
		fmt.Fprint(errOut, `
The same meters.yaml contains the BRIS configuration and dated readings:

bris:
  login: "your-email@example.com"
  password: "your-password"
  account: "0000000001"
  meters:
    water: "0000000000001"
    day: "К-000000001-Э"
    night: "К-000000001-Э"
    drinking: "0000001"
YYYY-MM-DD:
  water: 100
  day: 2000
  night: 300
  drinking: 400

Use your own account and serials; day and night normally share a serial.
The latest date must be in the current Europe/Moscow month, no later than today.
Readings are nonnegative whole integers: water in m³, day/night in kWh,
drinking in litres. Legacy DD-MM-YYYY dates are also accepted.
The gas readings and uslugi configuration are ignored.
Make the file private with chmod 600. It is never modified by this CLI.
Without --dry-run, all four readings are submitted and verified by reading back.
BRIS reported usage comes from the site's history table (matched by meter,
tariff, reading and date). Missing or ambiguous history is reported as unavailable.
The proposed delta uses the latest BRIS reading; YAML history is labelled separately.
`)
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
	input, err := loadInput(flags.Arg(0), now)
	if err != nil {
		return err
	}
	client, err := newBrisClient("https://lk.bris-cloud.ru", input.Credentials)
	if err != nil {
		return err
	}
	return client.submit(ctx, input.Reading, now, *dryRun, out)
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
