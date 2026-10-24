package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"
)

type mainAccount struct {
	Account  string `yaml:"account"`
	Flat     string `yaml:"flat"`
	Address  string `yaml:"address"`
	Informer string `yaml:"informer"`
}

type mainReading struct {
	Routing                mainAccount
	Date                   time.Time
	Electricity, Hot, Cold int64
	History                []mainReading
}

func loadMainReading(path string, now time.Time) (mainReading, error) {
	f, err := os.Open(path)
	if err != nil {
		return mainReading{}, fmt.Errorf("open meters %q: %w", path, err)
	}
	defer f.Close()
	return decodeMainReading(f, now)
}

func decodeMainReading(source io.Reader, now time.Time) (mainReading, error) {
	document, err := decodeMeterDocument(source)
	if err != nil {
		return mainReading{}, err
	}
	routing := document.Uslugi.Main
	if !regexp.MustCompile(`^[0-9]{10}$`).MatchString(routing.Account) {
		return mainReading{}, fmt.Errorf("uslugi.main.account %q must be a 10-digit personal account", routing.Account)
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(routing.Flat) {
		return mainReading{}, fmt.Errorf("uslugi.main.flat %q must be a numeric apartment number", routing.Flat)
	}
	if routing.Address == "" {
		return mainReading{}, fmt.Errorf("uslugi.main.address must contain the expected street and house")
	}
	if !regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`).MatchString(routing.Informer) {
		return mainReading{}, fmt.Errorf("uslugi.main.informer %q must be a saved ЖКХ informer UUID", routing.Informer)
	}
	var readings []mainReading
	for key, entry := range document.Entries {
		date, err := time.ParseInLocation(time.DateOnly, key, now.Location())
		if err != nil {
			date, err = time.ParseInLocation("02-01-2006", key, now.Location())
		}
		if err != nil {
			return mainReading{}, fmt.Errorf("invalid reading date %q; use YYYY-MM-DD", key)
		}
		for _, v := range []struct {
			name  string
			value *wholeNumber
		}{{"electricity", entry.Electricity}, {"hot", entry.Hot}, {"cold", entry.Cold}} {
			if v.value == nil {
				return mainReading{}, fmt.Errorf("reading %s is missing %s", key, v.name)
			}
			if *v.value < 0 {
				return mainReading{}, fmt.Errorf("negative %s reading %d on %s", v.name, *v.value, key)
			}
		}
		readings = append(readings, mainReading{Routing: routing, Date: date, Electricity: int64(*entry.Electricity), Hot: int64(*entry.Hot), Cold: int64(*entry.Cold)})
	}
	if len(readings) == 0 {
		return mainReading{}, fmt.Errorf("meters.yaml contains no dated readings")
	}
	sort.Slice(readings, func(i, j int) bool { return readings[i].Date.Before(readings[j].Date) })
	latest := readings[len(readings)-1]
	if latest.Date.Year() != now.Year() || latest.Date.Month() != now.Month() {
		return mainReading{}, fmt.Errorf("latest main reading %s must be in the current Moscow month %s", latest.Date.Format(time.DateOnly), now.Format("2006-01"))
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if latest.Date.After(today) {
		return mainReading{}, fmt.Errorf("latest main reading %s is after today %s", latest.Date.Format(time.DateOnly), today.Format(time.DateOnly))
	}
	for i := 1; i < len(readings); i++ {
		previous, current := readings[i-1], readings[i]
		if current.Date.Equal(previous.Date) {
			return mainReading{}, fmt.Errorf("duplicate reading date %s", current.Date.Format(time.DateOnly))
		}
		if current.Electricity < previous.Electricity || current.Hot < previous.Hot || current.Cold < previous.Cold {
			return mainReading{}, fmt.Errorf("main readings on %s decrease from %s", current.Date.Format(time.DateOnly), previous.Date.Format(time.DateOnly))
		}
	}
	latest.History = readings
	return latest, nil
}

func printMainHistory(out io.Writer, r mainReading) {
	if len(r.History) < 2 {
		return
	}
	previous := r.History[len(r.History)-2]
	endMonth := time.Date(r.Date.Year(), r.Date.Month(), 1, 0, 0, 0, 0, r.Date.Location())
	for month := time.Date(previous.Date.Year(), previous.Date.Month(), 1, 0, 0, 0, 0, r.Date.Location()).AddDate(0, 1, 0); month.Before(endMonth); month = month.AddDate(0, 1, 0) {
		fmt.Fprintf(out, "Reminder: you forgot to send readings for %s (no entry in meters.yaml).\n", month.Format("2006-01"))
	}
	fmt.Fprintf(out, "Usage since previous reading (%s -> %s; %d days): electricity %d kWh; hot %d m³; cold %d m³\n", previous.Date.Format(time.DateOnly), r.Date.Format(time.DateOnly), int(r.Date.Sub(previous.Date).Hours()/24), r.Electricity-previous.Electricity, r.Hot-previous.Hot, r.Cold-previous.Cold)
}
