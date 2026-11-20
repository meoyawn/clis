package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// runStatus reports, without submitting anything, whether the current Moscow
// month's YAML readings are already saved on the portal. It returns an error
// when any configured meter group is still pending, so scripts can branch on it.
func runStatus(ctx context.Context, path string, now time.Time, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open meters %q: %w", path, err)
	}
	document, err := decodeMeterDocument(f)
	f.Close()
	if err != nil {
		return err
	}
	hasGas, hasMain := document.Uslugi.Gas.Account != "", document.Uslugi.Main.Account != ""
	if !hasGas && !hasMain {
		return fmt.Errorf("meters.yaml has no uslugi.gas or uslugi.main routing")
	}
	month := now.Format("2006-01")
	var gas gasReading
	var main mainReading
	var pending []string
	needPortal := false
	if hasGas {
		gas, err = loadGasReading(path, now)
		if errors.Is(err, errNoCurrentMonth) {
			fmt.Fprintf(out, "gas: NOT SUBMITTED; meters.yaml has no %s reading\n", month)
			pending = append(pending, "gas")
			hasGas = false
		} else if err != nil {
			return err
		} else {
			needPortal = true
		}
	}
	if hasMain {
		main, err = loadMainReading(path, now)
		if errors.Is(err, errNoCurrentMonth) {
			fmt.Fprintf(out, "main: NOT SUBMITTED; meters.yaml has no %s reading\n", month)
			pending = append(pending, "main")
			hasMain = false
		} else if err != nil {
			return err
		} else {
			needPortal = true
		}
	}
	if needPortal {
		client, err := authenticatedClient()
		if err != nil {
			return err
		}
		if hasGas {
			p, err := client.gasPage(ctx, gas.Routing)
			if err != nil {
				return err
			}
			form, err := client.parseGas(p, gas.Routing)
			if err != nil {
				return err
			}
			if !reportGasStatus(out, form, gas) {
				pending = append(pending, "gas")
			}
		}
		if hasMain {
			p, err := client.mainPage(ctx, main.Routing)
			if err != nil {
				return err
			}
			form, err := client.parseMain(p, main.Routing)
			if err != nil {
				return err
			}
			submitted, err := reportMainStatus(out, form, main)
			if err != nil {
				return err
			}
			if !submitted {
				pending = append(pending, "main")
			}
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("%s readings not submitted: %s", month, strings.Join(pending, ", "))
	}
	fmt.Fprintf(out, "All %s readings are submitted.\n", month)
	return nil
}

// reportGasStatus treats gas as submitted when the portal's last transmitted
// value equals this month's YAML reading.
func reportGasStatus(out io.Writer, form gasForm, r gasReading) bool {
	if form.current == r.Value {
		fmt.Fprintf(out, "gas: submitted; portal transmitted %d matches YAML %s\n", form.current, r.Date.Format(time.DateOnly))
		return true
	}
	fmt.Fprintf(out, "gas: NOT SUBMITTED; portal transmitted %d, YAML %s has %d\n", form.current, r.Date.Format(time.DateOnly), r.Value)
	return false
}

// reportMainStatus treats main as submitted only when all three portal saved
// values equal this month's YAML readings.
func reportMainStatus(out io.Writer, form mainForm, r mainReading) (bool, error) {
	submitted := true
	var details []string
	for _, target := range mainTargets(r) {
		counter, err := matchingMainCounter(form, target.Service)
		if err != nil {
			return false, err
		}
		saved := "none"
		if counter.HasCurrent {
			saved = formatMeterDecimal(counter.Current)
		}
		match := counter.HasCurrent && counter.Current == target.Value*1000
		if !match {
			submitted = false
		}
		details = append(details, fmt.Sprintf("%s portal %s / YAML %d", target.Label, saved, target.Value))
	}
	verdict := "submitted"
	if !submitted {
		verdict = "NOT SUBMITTED"
	}
	fmt.Fprintf(out, "main: %s; %s (YAML %s)\n", verdict, strings.Join(details, "; "), r.Date.Format(time.DateOnly))
	return submitted, nil
}
