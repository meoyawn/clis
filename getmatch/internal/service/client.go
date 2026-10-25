package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/meoyawn/clis/getmatch/internal/domain"
)

const Origin = "https://getmatch.ru"

type Client struct {
	http    *http.Client
	headers http.Header
}

func NewClient(client *http.Client, headers http.Header) *Client {
	if headers == nil {
		headers = http.Header{}
	}
	return &Client{http: client, headers: headers.Clone()}
}

func (c *Client) get(ctx context.Context, path string, query url.Values, result any) error {
	u := Origin + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", Origin+"/salaries")
	started := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s after %s: %w; not retried", u, time.Since(started).Round(time.Millisecond), err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d, Retry-After=%q; stopped without retry", u, res.StatusCode, res.Header.Get("Retry-After"))
	}
	if !strings.Contains(res.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("GET %s returned %q instead of JSON; authentication or access challenge, stopped", u, res.Header.Get("Content-Type"))
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(result); err != nil {
		return fmt.Errorf("decode GET %s: %w", u, err)
	}
	return nil
}

func (c *Client) Specializations(ctx context.Context) ([]domain.Specialization, error) {
	var values []domain.Specialization
	if err := c.get(ctx, "/api/specializations", nil, &values); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("specialization catalog is empty; stopped")
	}
	seen := map[string]bool{}
	for _, v := range values {
		if v.ID <= 0 || v.Name == "" || v.Slug == "" || seen[v.Slug] {
			return nil, fmt.Errorf("invalid specialization catalog entry %q; stopped", v.Slug)
		}
		seen[v.Slug] = true
	}
	return values, nil
}
