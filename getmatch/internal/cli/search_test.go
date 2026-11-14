package cli

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/meoyawn/clis/getmatch/internal/domain"
)

func TestVacancySearchArguments(t *testing.T) {
	search, output, err := parseVacancySearch([]string{
		"--specialization", "golang", "--specialization", "python", "--skill", "Go", "--skill", " Golang ",
		"--remote", "--salary", "350000", "--level", "senior", "--level", "lead",
		"--page", "2", "--per-page", "50", "--output", "results.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(search.Specializations, []string{"golang", "python"}) || !reflect.DeepEqual(search.Skills, []string{"Go", " Golang "}) || !reflect.DeepEqual(search.Levels, []domain.Level{domain.Senior(), domain.Lead()}) || !search.Remote || search.Salary != 350000 || search.Page != 2 || search.PerPage != 50 || output != "results.json" {
		t.Fatalf("search arguments changed: %+v, output=%q", search, output)
	}
	defaults, _, err := parseVacancySearch(nil)
	if err != nil || defaults.Page != 0 || defaults.PerPage != 20 || defaults.Salary != 0 || defaults.Remote || len(defaults.Specializations) != 0 || len(defaults.Levels) != 0 {
		t.Fatalf("unfiltered search defaults changed: %+v, err=%v", defaults, err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"empty skill", []string{"--skill", " "}, `invalid skill " "`},
		{"empty specialization", []string{"--specialization", " "}, `invalid specialization " "`},
		{"unsupported salary", []string{"--salary", "349999"}, "invalid salary 349999"},
		{"negative page", []string{"--page", "-1"}, "invalid page -1"},
		{"overflowing page", []string{"--page", "9223372036854775807"}, "invalid page"},
		{"zero size", []string{"--per-page", "0"}, "invalid per-page 0"},
		{"large size", []string{"--per-page", "101"}, "invalid per-page 101"},
		{"invalid level", []string{"--level", "expert"}, `invalid level "expert"`},
		{"all level", []string{"--level", "all"}, `invalid search level "all"`},
		{"unsupported text", []string{"--text", "Go"}, "flag provided but not defined"},
		{"extra argument", []string{"Go"}, "unexpected search arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(context.Background(), append([]string{"--anonymous", "vacancies", "search"}, test.args...), &out)
			if err == nil || !strings.Contains(err.Error(), test.want) || out.Len() != 0 {
				t.Fatalf("expected %q before session/network access, got err=%v out=%q", test.want, err, out.String())
			}
		})
	}
}
