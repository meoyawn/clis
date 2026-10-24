package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMainFileReadingAndRouting(t *testing.T) {
	now := time.Date(2000, 10, 20, 12, 0, 0, 0, time.UTC)
	source := "uslugi:\n  main:\n    account: '0000000001'\n    flat: '1'\n    address: 'ул ПРИМЕРНАЯ, дом 1'\n    informer: '00000000-0000-4000-8000-000000000001'\n2000-10-18:\n  electricity: 1100\n  hot: 110\n  cold: 200\n2000-08-25:\n  electricity: 1000\n  hot: 100\n  cold: 200\n"
	for _, test := range []struct{ name, source, wantError string }{
		{"latest regardless of YAML order", source, ""},
		{"legacy dates", strings.Replace(source, "2000-10-18", "18-10-2000", 1), ""},
		{"missing routing", strings.Replace(source, "account: '0000000001'", "account: ''", 1), "10-digit personal account"},
		{"invalid informer", strings.Replace(source, "00000000-0000-4000-8000-000000000001", "bad", 1), "informer"},
		{"missing meter", strings.Replace(source, "  hot: 110\n", "", 1), "missing hot"},
		{"negative reading", strings.Replace(source, "electricity: 1100", "electricity: -1", 1), "negative electricity"},
		{"fractional YAML reading", strings.Replace(source, "electricity: 1100", "electricity: 1100.2", 1), "whole YAML integer"},
		{"decreased YAML reading", strings.Replace(source, "hot: 110", "hot: 99", 1), "decrease"},
		{"stale month", strings.Replace(source, "2000-10-18", "2000-09-18", 1), "current Moscow month"},
		{"future reading", strings.Replace(source, "2000-10-18", "2000-10-21", 1), "after today"},
		{"unknown meter field", strings.Replace(source, "hot: 110", "heat: 110", 1), "field heat not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, err := decodeMainReading(strings.NewReader(test.source), now)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("want %q, got %v", test.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Routing.Account != "0000000001" || r.Routing.Flat != "1" || r.Date.Format(time.DateOnly) != "2000-10-18" || r.Electricity != 1100 || r.Hot != 110 || r.Cold != 200 {
				t.Fatalf("wrong main routing/readings: %+v", r)
			}
			var output bytes.Buffer
			printMainHistory(&output, r)
			if !strings.Contains(output.String(), "forgot to send readings for 2000-09") || !strings.Contains(output.String(), "54 days): electricity 100 kWh; hot 10 m³; cold 0 m³") {
				t.Fatalf("missing full-gap consumption report: %s", &output)
			}
		})
	}
}
