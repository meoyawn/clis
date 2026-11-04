package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type VacancyID struct{ value string }
type ResumeID struct{ value string }
type ExperienceID struct{ value string }

var vacancyPattern = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)
var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,99}$`)

func target(value, prefix string) (string, error) {
	if !strings.Contains(value, "://") {
		return value, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "career.habr.com" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, prefix) {
		return "", fmt.Errorf("invalid Habr Career target %q; use an ID or canonical HTTPS role/profile URL", value)
	}
	return strings.TrimPrefix(u.Path, prefix), nil
}

func NewVacancyIDFromValue(value string) (VacancyID, error) {
	id, err := target(value, "/vacancies/")
	if err != nil {
		return VacancyID{}, err
	}
	if !vacancyPattern.MatchString(id) {
		return VacancyID{}, fmt.Errorf("invalid vacancy ID %q; expected a positive numeric ID", value)
	}
	return VacancyID{value: id}, nil
}

func NewResumeIDFromValue(value string) (ResumeID, error) {
	id, err := target(value, "/")
	if err != nil {
		return ResumeID{}, err
	}
	if !aliasPattern.MatchString(id) {
		return ResumeID{}, fmt.Errorf("invalid resume alias %q; use the stable account alias from profile get", value)
	}
	return ResumeID{value: id}, nil
}

func (id VacancyID) String() string { return id.value }
func (id ResumeID) String() string  { return id.value }

func NewExperienceIDFromValue(value string) (ExperienceID, error) {
	if !vacancyPattern.MatchString(value) {
		return ExperienceID{}, fmt.Errorf("invalid experience ID %q; use the native numeric row ID from resume experiences", value)
	}
	return ExperienceID{value: value}, nil
}

func (id ExperienceID) String() string { return id.value }
