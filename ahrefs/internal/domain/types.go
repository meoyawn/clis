package domain

import "fmt"

type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Target    string `json:"target"`
	LastCrawl *Crawl `json:"last_crawl,omitempty"`
}

type Crawl struct {
	ID        int    `json:"id"`
	Timestamp string `json:"timestamp,omitempty"`
	StartedAt string `json:"started_at"`
	Status    string `json:"status"`
}

type Changes struct {
	Delta   *int `json:"delta"`
	Added   *int `json:"added"`
	New     *int `json:"new"`
	Removed *int `json:"removed"`
	Missing *int `json:"missing"`
}

type Issue struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Level       string   `json:"level"`
	Category    string   `json:"category"`
	Active      *bool    `json:"active,omitempty"`
	Count       *int     `json:"count,omitempty"`
	Changes     *Changes `json:"changes,omitempty"`
	FilterID    string   `json:"filter_id,omitempty"`
	Columns     []string `json:"columns,omitempty"`
	Description string   `json:"description,omitempty"`
	HowToFix    string   `json:"how_to_fix,omitempty"`
}

type IssuesResult struct {
	ProjectID   string  `json:"project_id"`
	Crawl       string  `json:"crawl"`
	CompareWith string  `json:"compare_with,omitempty"`
	Issues      []Issue `json:"issues"`
}

type PagesResult struct {
	ProjectID   string           `json:"project_id"`
	Crawl       string           `json:"crawl"`
	CompareWith string           `json:"compare_with,omitempty"`
	IssueID     string           `json:"issue_id,omitempty"`
	FilterID    string           `json:"filter_id,omitempty"`
	Total       int              `json:"total"`
	Offset      int              `json:"offset"`
	NextOffset  *int             `json:"next_offset,omitempty"`
	Rows        []map[string]any `json:"rows"`
}

type Level struct{ value string }

const (
	errorLevel   = "error"
	warningLevel = "warning"
	noticeLevel  = "notice"
)

var allowedLevels = [...]string{errorLevel, warningLevel, noticeLevel}

func ErrorLevel() Level   { return Level{value: errorLevel} }
func WarningLevel() Level { return Level{value: warningLevel} }
func NoticeLevel() Level  { return Level{value: noticeLevel} }

func NewLevelFromValue(value string) (Level, error) {
	switch value {
	case errorLevel:
		return ErrorLevel(), nil
	case warningLevel:
		return WarningLevel(), nil
	case noticeLevel:
		return NoticeLevel(), nil
	default:
		return Level{}, fmt.Errorf("invalid level %q; allowed values: %v", value, allowedLevels)
	}
}

func (l Level) String() string { return l.value }

type Direction struct{ value string }

const (
	ascending  = "asc"
	descending = "desc"
)

var allowedDirections = [...]string{ascending, descending}

func Ascending() Direction  { return Direction{value: ascending} }
func Descending() Direction { return Direction{value: descending} }

func NewDirectionFromValue(value string) (Direction, error) {
	switch value {
	case ascending:
		return Ascending(), nil
	case descending:
		return Descending(), nil
	default:
		return Direction{}, fmt.Errorf("invalid sort direction %q; allowed values: %v", value, allowedDirections)
	}
}

func (d Direction) String() string { return d.value }
