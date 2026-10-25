package domain

import (
	"fmt"
	"strings"
)

type Level struct{ value string }

const (
	allLevelsValue = "all"
	juniorValue    = "junior"
	middleValue    = "middle"
	seniorValue    = "senior"
	leadValue      = "lead"
)

func AllLevels() Level { return Level{allLevelsValue} }
func Junior() Level    { return Level{juniorValue} }
func Middle() Level    { return Level{middleValue} }
func Senior() Level    { return Level{seniorValue} }
func Lead() Level      { return Level{leadValue} }
func NewLevelFromValue(v string) (Level, error) {
	switch v {
	case allLevelsValue:
		return AllLevels(), nil
	case juniorValue:
		return Junior(), nil
	case middleValue:
		return Middle(), nil
	case seniorValue:
		return Senior(), nil
	case leadValue:
		return Lead(), nil
	}
	return Level{}, fmt.Errorf("invalid level %q: use all, junior, middle, senior or lead", v)
}
func (l Level) String() string { return l.value }

// These aliases supply search terms, not invented Glassdoor categories. The
// CLI must still resolve an exact native autocomplete entry before estimating.
func TitleForRole(role string, level Level) (string, error) {
	if _, err := NewLevelFromValue(level.String()); err != nil {
		return "", err
	}
	titles := map[string]string{
		"go": "Golang Developer", "golang": "Golang Developer",
		"android": "Android Developer", "flutter": "Flutter Developer",
		"frontend": "Frontend Developer", "backend": "Backend Developer",
		"fullstack": "Full Stack Developer", "software": "Software Engineer",
	}
	title := titles[strings.ToLower(strings.TrimSpace(role))]
	if title == "" {
		title = strings.TrimSpace(role)
	}
	if title == "" {
		return "", fmt.Errorf("role is empty")
	}
	switch level.value {
	case juniorValue:
		title = "Junior " + title
	case seniorValue:
		title = "Senior " + title
	case leadValue:
		title = "Lead " + title
	}
	return title, nil
}
