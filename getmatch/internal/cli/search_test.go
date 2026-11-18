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
		"--location", "regions_ru", "--remote", "--location", "remote", "--period", "30d", "--salary", "350000", "--level", "senior", "--level", "lead",
		"--page", "2", "--per-page", "50", "--output", "results.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(search.Specializations, []string{"golang", "python"}) || !reflect.DeepEqual(search.Skills, []string{"Go", " Golang "}) || !reflect.DeepEqual(search.Levels, []domain.Level{domain.Senior(), domain.Lead()}) || !reflect.DeepEqual(search.Locations, []string{"regions_ru", "remote"}) || search.Period != "30d" || search.Salary != 350000 || search.Page != 2 || search.PerPage != 50 || output != "results.json" {
		t.Fatalf("search arguments changed: %+v, output=%q", search, output)
	}
	defaults, _, err := parseVacancySearch(nil)
	if err != nil || defaults.Page != 0 || defaults.PerPage != 20 || defaults.Salary != 0 || len(defaults.Locations) != 0 || defaults.Period != "" || len(defaults.Specializations) != 0 || len(defaults.Levels) != 0 {
		t.Fatalf("unfiltered search defaults changed: %+v, err=%v", defaults, err)
	}
	fromURL, _, err := parseVacancySearch([]string{"--per-page", "100", "--skill", "Go", "--url",
		"https://getmatch.ru/vacancies?p=2&sa=250000&pa=all&s=wih&l=regions_ru&l=remote&sp=golang&sp=js_backend&se=senior"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromURL, domain.VacancySearchOptions{
		Specializations: []string{"golang", "js_backend"}, Skills: []string{"Go"}, Locations: []string{"regions_ru", "remote"},
		Salary: 250000, Period: "all", Levels: []domain.Level{domain.Senior()}, Page: 1, PerPage: 100, S: "wih",
	}) {
		t.Fatalf("native search URL mapped differently: %+v", fromURL)
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
		{"invalid location", []string{"--location", "russia"}, `invalid location "russia"`},
		{"invalid period", []string{"--period", "2d"}, `invalid period "2d"`},
		{"url with filter flag", []string{"--remote", "--url", "https://getmatch.ru/vacancies?sp=golang"}, "drop [--remote]"},
		{"url foreign host", []string{"--url", "https://example.org/vacancies?sp=golang"}, "invalid search URL"},
		{"url unknown parameter", []string{"--url", "https://getmatch.ru/vacancies?q=go"}, `unsupported search URL parameter "q"`},
		{"url invalid location", []string{"--url", "https://getmatch.ru/vacancies?l=bogus"}, `invalid location "bogus"`},
		{"url zero page", []string{"--url", "https://getmatch.ru/vacancies?p=0"}, "invalid search URL p"},
		{"url repeated salary", []string{"--url", "https://getmatch.ru/vacancies?sa=150000&sa=250000"}, `repeats "sa"`},
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
