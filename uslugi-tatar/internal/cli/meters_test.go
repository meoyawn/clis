package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGasFileRoutingAndValidation(t *testing.T) {
	now := time.Date(2000, 10, 19, 12, 0, 0, 0, time.UTC)
	source := "uslugi:\n  gas:\n    account: '000000000001'\n    meter: '100001'\n2000-10-18:\n  electricity: 1100\n  cold: 200\n  hot: 110\n  gas: 123\n2000-08-25:\n  gas: 100\n"
	for _, test := range []struct{ name, source, wantError string }{
		{"primary account", source, ""},
		{"main routing alongside gas", strings.Replace(source, "  gas:", "  main:\n    account: '0000000001'\n    flat: '1'\n    address: 'ул ПРИМЕРНАЯ, дом 1'\n    informer: '00000000-0000-4000-8000-000000000001'\n  gas:", 1), ""},
		{"alternate account independent of path", strings.NewReplacer("000000000001", "000000000002", "100001", "100002", "123", "234", "100", "200").Replace(source), ""},
		{"missing account", strings.Replace(source, "000000000001", "", 1), "12-digit account"},
		{"missing meter", strings.Replace(source, "100001", "", 1), "numeric serial"},
		{"missing gas", strings.Replace(source, "  gas: 123\n", "", 1), "missing gas"},
		{"stale month", strings.Replace(source, "2000-10-18", "2000-09-18", 1), "current Moscow month"},
		{"future date", strings.Replace(source, "2000-10-18", "2000-10-20", 1), "after today"},
		{"fraction", strings.Replace(source, "gas: 123", "gas: 123.1", 1), "whole YAML integer"},
		{"decrease", strings.Replace(source, "gas: 123", "gas: 99", 1), "below preceding reading"},
		{"six digit overflow", strings.Replace(source, "gas: 123", "gas: 1000000", 1), "six nonnegative"},
		{"unknown field", strings.Replace(source, "gas: 123", "gass: 123", 1), "field gass not found"},
		{"duplicate date", source + "2000-10-18:\n  gas: 123\n", "already defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, err := decodeGasReading(strings.NewReader(test.source), now)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("want %q, got %v", test.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			printGasHistory(&output, r)
			if !strings.Contains(output.String(), "forgot to send readings for 2000-09") || !strings.Contains(output.String(), "54 days)") {
				t.Fatalf("missing gap report: %s", &output)
			}
			if test.name == "primary account" && (r.Routing.Account != "000000000001" || r.Routing.Meter != "100001" || r.Value != 123) {
				t.Fatalf("wrong primary account route/value: %+v", r)
			}
			if test.name == "alternate account independent of path" && (r.Routing.Account != "000000000002" || r.Routing.Meter != "100002" || r.Value != 234) {
				t.Fatalf("wrong alternate account route/value: %+v", r)
			}
		})
	}
}
