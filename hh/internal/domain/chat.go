package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ChatID identifies an explicitly selected HH conversation.
type ChatID struct{ value string }

var chatNumber = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)

// NewChatIDFromValue accepts a numeric ID or an HTTPS hh.ru chat URL.
func NewChatIDFromValue(value string) (ChatID, error) {
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
			(u.Hostname() != "hh.ru" && !strings.HasSuffix(u.Hostname(), ".hh.ru")) {
			return ChatID{}, fmt.Errorf("invalid chat URL: expected an HTTPS hh.ru URL without credentials")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 2 || parts[0] != "chat" {
			return ChatID{}, fmt.Errorf("invalid chat path %q: expected /chat/ID", u.Path)
		}
		value = parts[1]
	}
	if !chatNumber.MatchString(value) {
		return ChatID{}, fmt.Errorf("invalid chat ID %q: expected a positive numeric hh.ru chat ID", value)
	}
	return ChatID{value: value}, nil
}

func (id ChatID) String() string { return id.value }
