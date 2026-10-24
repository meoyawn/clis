package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"
)

type reading struct {
	Date     time.Time
	Day      int64
	Night    int64
	Water    int64
	Drinking int64
	History  []reading
}

type yamlReading struct {
	Day      *wholeNumber `yaml:"day"`
	Night    *wholeNumber `yaml:"night"`
	Water    *wholeNumber `yaml:"water"`
	Drinking *wholeNumber `yaml:"drinking"`
	Gas      *wholeNumber `yaml:"gas"`
}

type wholeNumber int64

func (value *wholeNumber) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag != "!!int" {
		return fmt.Errorf("meter reading %q must be a whole YAML integer", node.Value)
	}
	var number int64
	if err := node.Decode(&number); err != nil {
		return err
	}
	*value = wholeNumber(number)
	return nil
}

func loadReading(path string, now time.Time) (reading, error) {
	f, err := os.Open(path)
	if err != nil {
		return reading{}, fmt.Errorf("open readings %q: %w", path, err)
	}
	defer f.Close()
	return decodeReading(f, now)
}

func decodeReading(source io.Reader, now time.Time) (reading, error) {
	var document struct {
		Uslugi  yaml.Node              `yaml:"uslugi"`
		Entries map[string]yamlReading `yaml:",inline"`
	}
	d := yaml.NewDecoder(source)
	d.KnownFields(true)
	if err := d.Decode(&document); err != nil {
		return reading{}, fmt.Errorf("decode meters.yaml: %w", err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return reading{}, fmt.Errorf("meters.yaml must contain exactly one YAML document")
	}
	entries := document.Entries
	if len(entries) == 0 {
		return reading{}, fmt.Errorf("meters.yaml contains no dated readings")
	}
	var readings []reading
	for key, entry := range entries {
		date, err := time.ParseInLocation(time.DateOnly, key, now.Location())
		if err != nil {
			// Accept the date format used by the older meter script as well.
			date, err = time.ParseInLocation("02-01-2006", key, now.Location())
		}
		if err != nil {
			return reading{}, fmt.Errorf("invalid reading date %q; use YYYY-MM-DD", key)
		}
		values := []struct {
			name  string
			value *wholeNumber
		}{{"day", entry.Day}, {"night", entry.Night}, {"water", entry.Water}, {"drinking", entry.Drinking}}
		for _, v := range values {
			if v.value == nil {
				return reading{}, fmt.Errorf("reading %s is missing %s", key, v.name)
			}
			if *v.value < 0 {
				return reading{}, fmt.Errorf("reading %s has negative %s: %d", key, v.name, *v.value)
			}
		}
		readings = append(readings, reading{Date: date, Day: int64(*entry.Day), Night: int64(*entry.Night), Water: int64(*entry.Water), Drinking: int64(*entry.Drinking)})
	}
	sort.Slice(readings, func(i, j int) bool { return readings[i].Date.Before(readings[j].Date) })
	latest := readings[len(readings)-1]
	if latest.Date.Year() != now.Year() || latest.Date.Month() != now.Month() {
		return reading{}, fmt.Errorf("latest reading %s must be in the current Moscow month %s", latest.Date.Format(time.DateOnly), now.Format("2006-01"))
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if latest.Date.After(today) {
		return reading{}, fmt.Errorf("latest reading %s is after today %s", latest.Date.Format(time.DateOnly), today.Format(time.DateOnly))
	}
	for i := 1; i < len(readings); i++ {
		previous, current := readings[i-1], readings[i]
		if previous.Date.Equal(current.Date) {
			return reading{}, fmt.Errorf("duplicate reading date %s in different date formats", current.Date.Format(time.DateOnly))
		}
		if current.Day < previous.Day || current.Night < previous.Night || current.Water < previous.Water || current.Drinking < previous.Drinking {
			return reading{}, fmt.Errorf("readings on %s decrease from %s", current.Date.Format(time.DateOnly), previous.Date.Format(time.DateOnly))
		}
	}
	latest.History = readings
	return latest, nil
}

func printHistoricalUsage(out io.Writer, current reading, now time.Time) {
	lastMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -1, 0)
	lastIndex := -1
	for i, r := range current.History {
		if r.Date.Year() == lastMonth.Year() && r.Date.Month() == lastMonth.Month() {
			lastIndex = i
		}
	}
	if lastIndex < 0 {
		fmt.Fprintf(out, "Reminder: you forgot to send readings for %s (no entry in meters.yaml).\n", lastMonth.Format("2006-01"))
	} else if lastIndex == 0 {
		fmt.Fprintf(out, "Last month's recorded usage (%s): unavailable (missing reading or baseline).\n", lastMonth.Format("2006-01"))
	} else {
		end := current.History[lastIndex]
		var start reading
		for i := lastIndex - 1; i >= 0; i-- {
			if current.History[i].Date.Before(lastMonth) {
				start = current.History[i]
				break
			}
		}
		if start.Date.IsZero() {
			fmt.Fprintf(out, "Last month's recorded usage (%s): unavailable (missing baseline).\n", lastMonth.Format("2006-01"))
		} else {
			printUsageInterval(out, "Last month's recorded usage", start, end)
		}
	}
	if len(current.History) >= 2 {
		printUsageInterval(out, "Usage since previous reading", current.History[len(current.History)-2], current)
	}
}

func printUsageInterval(out io.Writer, label string, start, end reading) {
	fmt.Fprintf(out, "%s (%s -> %s; %d days): water %d m³; day %d kWh; night %d kWh; drinking %d L\n", label, start.Date.Format(time.DateOnly), end.Date.Format(time.DateOnly), int(end.Date.Sub(start.Date).Hours()/24), end.Water-start.Water, end.Day-start.Day, end.Night-start.Night, end.Drinking-start.Drinking)
}
