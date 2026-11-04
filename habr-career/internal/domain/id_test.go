package domain_test

import (
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/domain"
)

func TestTargetsCannotChangeRequestDestination(t *testing.T) {
	for _, input := range []string{"1000000000", "https://career.habr.com/vacancies/1000000000"} {
		id, err := domain.NewVacancyIDFromValue(input)
		if err != nil || id.String() != "1000000000" {
			t.Fatalf("valid vacancy %q: %v %v", input, id, err)
		}
	}
	for _, input := range []string{"", "0", "-1", "../responses", "https://foreign.test/vacancies/1000000000", "https://career.habr.com:443/vacancies/1000000000", "https://user@career.habr.com/vacancies/1000000000", "https://career.habr.com/vacancies/1000000000?other=1", "https://career.habr.com/vacancies/1000000000#apply", "https://career.habr.com/vacancies/1000000000/extra"} {
		if _, err := domain.NewVacancyIDFromValue(input); err == nil {
			t.Errorf("unsafe vacancy accepted: %q", input)
		}
	}
	for _, input := range []string{"synthetic-user", "https://career.habr.com/synthetic-user"} {
		id, err := domain.NewResumeIDFromValue(input)
		if err != nil || id.String() != "synthetic-user" {
			t.Fatalf("valid alias %q: %v %v", input, id, err)
		}
	}
	for _, input := range []string{"", "../users/me", "https://foreign.test/alias", "https://career.habr.com/alias/print", "alias?x=1", "https://career.habr.com/%61lias"} {
		if _, err := domain.NewResumeIDFromValue(input); err == nil {
			t.Errorf("unsafe alias accepted: %q", input)
		}
	}
}
