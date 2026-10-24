package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"
)

type gasAccount struct {
	Account string `yaml:"account"`
	Meter   string `yaml:"meter"`
}

type gasReading struct {
	Routing gasAccount
	Date    time.Time
	Value   int64
	History []gasReading
}

type wholeNumber int64

func (v *wholeNumber) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag != "!!int" {
		return fmt.Errorf("reading %q must be a whole YAML integer", n.Value)
	}
	var value int64
	if err := n.Decode(&value); err != nil {
		return err
	}
	*v = wholeNumber(value)
	return nil
}

type meterEntry struct {
	Gas         *wholeNumber `yaml:"gas"`
	Electricity *wholeNumber `yaml:"electricity"`
	Cold        *wholeNumber `yaml:"cold"`
	Hot         *wholeNumber `yaml:"hot"`
	Day         *wholeNumber `yaml:"day"`
	Night       *wholeNumber `yaml:"night"`
	Water       *wholeNumber `yaml:"water"`
	Drinking    *wholeNumber `yaml:"drinking"`
}

func loadGasReading(path string, now time.Time) (gasReading, error) {
	f, err := os.Open(path)
	if err != nil {
		return gasReading{}, fmt.Errorf("open meters %q: %w", path, err)
	}
	defer f.Close()
	return decodeGasReading(f, now)
}

type meterDocument struct {
	Uslugi struct {
		Gas  gasAccount  `yaml:"gas"`
		Main mainAccount `yaml:"main"`
	} `yaml:"uslugi"`
	Entries map[string]meterEntry `yaml:",inline"`
}

func decodeMeterDocument(source io.Reader) (meterDocument, error) {
	var document meterDocument
	d := yaml.NewDecoder(source)
	d.KnownFields(true)
	if err := d.Decode(&document); err != nil {
		return document, fmt.Errorf("decode meters.yaml: %w", err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return document, fmt.Errorf("meters.yaml must contain exactly one document")
	}
	return document, nil
}

func decodeGasReading(source io.Reader, now time.Time) (gasReading, error) {
	document, err := decodeMeterDocument(source)
	if err != nil {
		return gasReading{}, err
	}
	routing := document.Uslugi.Gas
	if !regexp.MustCompile(`^[0-9]{12}$`).MatchString(routing.Account) {
		return gasReading{}, fmt.Errorf("uslugi.gas.account %q must be a quoted 12-digit account number", routing.Account)
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(routing.Meter) {
		return gasReading{}, fmt.Errorf("uslugi.gas.meter %q must be a quoted numeric serial number", routing.Meter)
	}
	var readings []gasReading
	for key, entry := range document.Entries {
		date, err := time.ParseInLocation(time.DateOnly, key, now.Location())
		if err != nil {
			date, err = time.ParseInLocation("02-01-2006", key, now.Location())
		}
		if err != nil {
			return gasReading{}, fmt.Errorf("invalid reading date %q; use YYYY-MM-DD", key)
		}
		if entry.Gas == nil {
			return gasReading{}, fmt.Errorf("reading %s is missing gas", key)
		}
		value := int64(*entry.Gas)
		if value < 0 || value > 999999 {
			return gasReading{}, fmt.Errorf("gas reading %d on %s must fit six nonnegative whole digits", value, key)
		}
		readings = append(readings, gasReading{Routing: routing, Date: date, Value: value})
	}
	if len(readings) == 0 {
		return gasReading{}, fmt.Errorf("meters.yaml contains no dated readings")
	}
	sort.Slice(readings, func(i, j int) bool { return readings[i].Date.Before(readings[j].Date) })
	latest := readings[len(readings)-1]
	if latest.Date.Year() != now.Year() || latest.Date.Month() != now.Month() {
		return gasReading{}, fmt.Errorf("latest gas reading %s must be in the current Moscow month %s", latest.Date.Format(time.DateOnly), now.Format("2006-01"))
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if latest.Date.After(today) {
		return gasReading{}, fmt.Errorf("latest gas reading %s is after today %s", latest.Date.Format(time.DateOnly), today.Format(time.DateOnly))
	}
	for i := 1; i < len(readings); i++ {
		if readings[i].Date.Equal(readings[i-1].Date) {
			return gasReading{}, fmt.Errorf("duplicate date %s", readings[i].Date.Format(time.DateOnly))
		}
		if readings[i].Value < readings[i-1].Value {
			return gasReading{}, fmt.Errorf("gas reading %d on %s is below preceding reading %d", readings[i].Value, readings[i].Date.Format(time.DateOnly), readings[i-1].Value)
		}
	}
	latest.History = readings
	return latest, nil
}

func printGasHistory(out io.Writer, r gasReading) {
	if len(r.History) < 2 {
		return
	}
	previous := r.History[len(r.History)-2]
	firstOfMonth := time.Date(r.Date.Year(), r.Date.Month(), 1, 0, 0, 0, 0, r.Date.Location())
	for month := time.Date(previous.Date.Year(), previous.Date.Month(), 1, 0, 0, 0, 0, r.Date.Location()).AddDate(0, 1, 0); month.Before(firstOfMonth); month = month.AddDate(0, 1, 0) {
		fmt.Fprintf(out, "Reminder: you forgot to send readings for %s (no entry in meters.yaml).\n", month.Format("2006-01"))
	}
	fmt.Fprintf(out, "Usage since previous reading (%s -> %s; %d days): %d m³\n", previous.Date.Format(time.DateOnly), r.Date.Format(time.DateOnly), int(r.Date.Sub(previous.Date).Hours()/24), r.Value-previous.Value)
}
