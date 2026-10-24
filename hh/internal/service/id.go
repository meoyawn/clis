package service

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ResumeID is the public, stable hash shared by edit and employer-view URLs.
// Numeric database IDs and profile experience IDs are deliberately different.
type ResumeID struct{ value string }

var resumeHash = regexp.MustCompile(`^[a-f0-9]{38}$`)

func NewResumeIDFromValue(value string) (ResumeID, error) {
	original := value
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || !IsHHHost(u.Hostname()) || u.User != nil {
			if u != nil && u.User != nil {
				u.User = url.User("[redacted]")
				original = u.String()
			}
			return ResumeID{}, fmt.Errorf("invalid resume URL %q: expected an HTTPS hh.ru resume URL without credentials", original)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		switch {
		case len(parts) == 2 && parts[0] == "resume":
			value = parts[1]
		case len(parts) >= 3 && parts[0] == "resume" && parts[1] == "edit":
			value = parts[2]
		default:
			return ResumeID{}, fmt.Errorf("invalid resume URL path %q: expected /resume/HASH", u.Path)
		}
	}
	if !resumeHash.MatchString(value) {
		return ResumeID{}, fmt.Errorf("invalid resume ID %q: expected the 38-character lowercase hash from /resume/HASH", original)
	}
	return ResumeID{value: value}, nil
}

func (id ResumeID) String() string { return id.value }

type VacancyID struct{ value string }

var vacancyNumber = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)

func NewVacancyIDFromValue(value string) (VacancyID, error) {
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || !IsHHHost(u.Hostname()) || u.User != nil || u.Port() != "" {
			return VacancyID{}, fmt.Errorf("invalid vacancy URL: expected an HTTPS hh.ru URL without credentials")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 2 || parts[0] != "vacancy" {
			return VacancyID{}, fmt.Errorf("invalid vacancy path %q: expected /vacancy/ID", u.Path)
		}
		value = parts[1]
	}
	if !vacancyNumber.MatchString(value) {
		return VacancyID{}, fmt.Errorf("invalid vacancy ID %q: expected a positive numeric hh.ru vacancy ID", value)
	}
	return VacancyID{value: value}, nil
}

func (id VacancyID) String() string { return id.value }

func IsHHHost(host string) bool {
	return host == "hh.ru" || strings.HasSuffix(host, ".hh.ru")
}
