package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type VacancyID struct{ value string }

var vacancyNumber = regexp.MustCompile(`^[1-9][0-9]*$`)

func NewVacancyIDFromValue(value string) (VacancyID, error) {
	v := value
	if strings.HasPrefix(v, "https://") {
		u, err := url.Parse(v)
		if err != nil || u.Host != "getmatch.ru" || !strings.HasPrefix(u.Path, "/vacancies/") || u.User != nil {
			return VacancyID{}, fmt.Errorf("invalid Getmatch vacancy URL %q", value)
		}
		v = strings.SplitN(strings.TrimPrefix(u.Path, "/vacancies/"), "-", 2)[0]
	}
	if !vacancyNumber.MatchString(v) {
		return VacancyID{}, fmt.Errorf("invalid vacancy ID %q: expected a positive number or Getmatch vacancy URL", value)
	}
	return VacancyID{value: v}, nil
}

func (v VacancyID) String() string { return v.value }
