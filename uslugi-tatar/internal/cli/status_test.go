package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/uslugi-tatar/internal/domain"
)

func TestStatusVerdicts(t *testing.T) {
	gas := gasReading{Value: 123, Date: time.Date(2000, 10, 18, 0, 0, 0, 0, time.UTC)}
	if !reportGasStatus(&bytes.Buffer{}, gasForm{current: 123}, gas) {
		t.Error("gas with matching transmitted value should be submitted")
	}
	if reportGasStatus(&bytes.Buffer{}, gasForm{current: 120}, gas) {
		t.Error("gas with older transmitted value should be pending")
	}

	main := mainReading{Date: gas.Date, Electricity: 1100, Hot: 110, Cold: 200}
	form := func(cold int64, hasCold bool) mainForm {
		return mainForm{Counters: []mainCounter{
			{Service: domain.Electricity(), Serial: "1", Current: 1100000, HasCurrent: true},
			{Service: domain.HotWater(), Serial: "2", Current: 110000, HasCurrent: true},
			{Service: domain.ColdWater(), Serial: "3", Current: cold, HasCurrent: hasCold},
		}}
	}
	for _, test := range []struct {
		name string
		form mainForm
		want bool
	}{
		{"all saved values match", form(200000, true), true},
		{"one value differs", form(199000, true), false},
		{"blank saved value", form(0, false), false},
	} {
		got, err := reportMainStatus(&bytes.Buffer{}, test.form, main)
		if err != nil || got != test.want {
			t.Errorf("%s: got %v, %v; want %v", test.name, got, err, test.want)
		}
	}
}

func TestStatusStaleYAMLIsPendingWithoutPortal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meters.yaml")
	source := "uslugi:\n  gas:\n    account: '000000000001'\n    meter: '100001'\n2000-09-18:\n  gas: 123\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runStatus(context.Background(), path, time.Date(2000, 10, 20, 12, 0, 0, 0, time.UTC), &out)
	if err == nil || !strings.Contains(err.Error(), "2000-10 readings not submitted: gas") || !strings.Contains(out.String(), "no 2000-10 reading") {
		t.Fatalf("want pending gas without portal access, got %v: %s", err, &out)
	}
}
