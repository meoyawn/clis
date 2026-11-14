package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseVacancySearch(t *testing.T) {
	const text = `DESCRIPTION:(Go OR Golang) NOT intern`
	search, output, err := parseVacancySearch([]string{"--text", text, "--skill", "Go", "--skill", "Golang", "--remote", "--area", "1", "--area", "2", "--page", "2", "--per-page", "50", "--output", "results.json"})
	if err != nil {
		t.Fatal(err)
	}
	if search.Text != text || !reflect.DeepEqual(search.Skills, []string{"Go", "Golang"}) || !reflect.DeepEqual(search.Areas, []string{"1", "2"}) || !search.Remote || search.Page != 2 || search.PerPage != 50 || output != "results.json" {
		t.Fatalf("search options lost: %+v, output=%q", search, output)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"no-query", nil, "--text QUERY or --skill NAME"},
		{"empty-skill", []string{"--skill", " "}, `invalid skill " "`},
		{"bad-area", []string{"--text", "Go", "--area", "Kazan"}, `invalid area "Kazan"`},
		{"negative-page", []string{"--text", "Go", "--page", "-1"}, "invalid page -1"},
		{"past-window", []string{"--text", "Go", "--page", "100"}, "invalid page 100"},
		{"zero-size", []string{"--text", "Go", "--per-page", "0"}, "invalid per-page 0"},
		{"large-size", []string{"--text", "Go", "--per-page", "101"}, "invalid per-page 101"},
		{"extra-argument", []string{"--text", "Go", "extra"}, "unexpected search arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseVacancySearch(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q before cookie/network access, got %v", test.want, err)
			}
		})
	}
}
