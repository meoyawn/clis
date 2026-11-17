package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testBrisConfig = `bris:
  login: test@example.com
  password: test-password
  account: "0000000001"
  meters:
    water: "0000000000001"
    day: К-000000001-Э
    night: К-000000001-Э
    drinking: "0000001"
`

func TestLatestReadingValidation(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 10, 19, 0, 30, 0, 0, zone)
	current := "2030-10-19:\n  day: 2060\n  night: 320\n  water: 102\n  drinking: 405\n  gas: 500\n"
	previous := "2030-08-25:\n  day: 2000\n  night: 300\n  water: 100\n  drinking: 400\n"
	for _, test := range []struct {
		name, source, wantError string
	}{
		{"latest regardless of order", current + previous, ""},
		{"legacy date", strings.Replace(current, "2030-10-19", "19-10-2030", 1), ""},
		{"gas optional", strings.Replace(current, "  gas: 500\n", "", 1), ""},
		{"apartment routing metadata", "uslugi:\n  gas:\n    account: '000000000001'\n    meter: '000001'\n" + current, ""},
		{"BRIS configuration", testBrisConfig + current, ""},
		{"invalid BRIS credential type", strings.Replace(testBrisConfig, "password: test-password", "password: {secret-not-for-errors: value}", 1) + current, "invalid bris configuration"},
		{"unknown BRIS credential field", strings.Replace(testBrisConfig, "password:", "secret-not-for-errors:", 1) + current, "invalid bris configuration"},
		{"previous month", previous, "current Moscow month 2030-10"},
		{"same month previous year", strings.Replace(current, "2030-10-19", "2029-10-19", 1), "current Moscow month"},
		{"future month", strings.Replace(current, "2030-10-19", "2030-11-01", 1), "current Moscow month"},
		{"future day", strings.Replace(current, "2030-10-19", "2030-10-20", 1), "after today"},
		{"invalid date", strings.Replace(current, "2030-10-19", "2030-02-30", 1), "invalid reading date"},
		{"missing reading", strings.Replace(current, "  water: 102\n", "", 1), "missing water"},
		{"fractional reading", strings.Replace(current, "water: 102", "water: 102.9", 1), "whole YAML integer"},
		{"quoted reading", strings.Replace(current, "water: 102", "water: '102'", 1), "whole YAML integer"},
		{"negative reading", strings.Replace(current, "water: 102", "water: -1", 1), "negative water"},
		{"decrease", previous + strings.Replace(current, "water: 102", "water: 99", 1), "decrease"},
		{"unknown field", current + "  typo: 7\n", "field typo not found"},
		{"duplicate YAML date", current + current, "already defined"},
		{"duplicate date formats", current + strings.Replace(current, "2030-10-19", "19-10-2030", 1), "duplicate reading date"},
		{"multiple documents", current + "---\n" + previous, "exactly one YAML document"},
		{"empty file", "", "decode meters.yaml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := decodeInput(strings.NewReader(test.source), now)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected error containing %q, got %v", test.wantError, err)
				}
				if strings.Contains(err.Error(), "secret-not-for-errors") {
					t.Fatalf("error leaked a credential value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := input.Reading
			if got.Date.Format(time.DateOnly) != "2030-10-19" || got.Water != 102 || got.Day != 2060 || got.Night != 320 || got.Drinking != 405 {
				t.Fatalf("unexpected latest reading: %+v", got)
			}
		})
	}
}

func TestMeterFileMustBePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meters.yaml")
	source := testBrisConfig + "2030-10-19:\n  day: 2060\n  night: 320\n  water: 102\n  drinking: 405\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 10, 19, 12, 0, 0, 0, time.UTC)
	if _, err := loadInput(path, now); err == nil || !strings.Contains(err.Error(), "must be private; run chmod 600") {
		t.Fatalf("expected private-file requirement, got %v", err)
	}
}

func TestHistoricalUsageSurvivesResubmissionAndMissingMonth(t *testing.T) {
	now := time.Date(2030, 10, 19, 12, 0, 0, 0, time.UTC)
	august := "2030-08-25:\n  day: 2000\n  night: 300\n  water: 100\n  drinking: 400\n"
	september := "2030-09-25:\n  day: 2040\n  night: 310\n  water: 102\n  drinking: 403\n"
	october := "2030-10-19:\n  day: 2060\n  night: 320\n  water: 102\n  drinking: 405\n"
	for _, test := range []struct{ name, source, want, reminder string }{
		{"missing month whole gap", august + october, "2030-08-25 -> 2030-10-19; 55 days): water 2 m³; day 60 kWh; night 20 kWh; drinking 5 L", "forgot to send readings for 2030-09"},
		{"previous month's recorded usage", august + september + october, "Last month's recorded usage (2030-08-25 -> 2030-09-25; 31 days): water 2 m³; day 40 kWh; night 10 kWh; drinking 3 L", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := decodeInput(strings.NewReader(test.source), now)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			printHistoricalUsage(&out, input.Reading, now)
			if !strings.Contains(out.String(), test.want) {
				t.Fatalf("missing usage %q in %s", test.want, &out)
			}
			if test.reminder != "" && !strings.Contains(out.String(), test.reminder) {
				t.Fatalf("missing reminder in %s", &out)
			}
			if test.reminder == "" && strings.Contains(out.String(), "forgot") {
				t.Fatalf("false missing-month reminder: %s", &out)
			}
		})
	}
}
