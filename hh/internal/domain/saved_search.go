package domain

import (
	"fmt"
	"strings"
)

type SavedSearchOptions struct {
	Text   string
	Remote bool
}

func (o SavedSearchOptions) Validate() error {
	if strings.TrimSpace(o.Text) == "" {
		return fmt.Errorf("invalid saved-search text %q: expected a nonempty HH query", o.Text)
	}
	return nil
}
