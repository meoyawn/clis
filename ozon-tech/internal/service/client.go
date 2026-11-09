package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const apiPath = "/p-api/ozon-tech/vacancy/"

type Client struct {
	http *http.Client
}

func NewClient(client *http.Client) *Client {
	owned := *client
	owned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &owned}
}

func (c *Client) request(ctx context.Context, path string, payload any, result any) error {
	u, err := url.Parse(Origin + path)
	if err != nil || u.Scheme != "https" || u.Host != "ozon.tech" || u.User != nil || !strings.HasPrefix(u.Path, apiPath) {
		return fmt.Errorf("invalid Ozon Tech API path %q", path)
	}
	method := http.MethodGet
	var body []byte
	if payload != nil {
		method = http.MethodPost
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for attempt := 0; attempt < 4; attempt++ {
		if seen[u.String()] {
			break
		}
		seen[u.String()] = true
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", Origin)
		req.Header.Set("Referer", Origin+"/vacancies/")
		req.Header.Set("X-O3-App-Name", "ozon-tech-ui")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("request %s: %w", u.Path, err)
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(b) > 32<<20 {
			return fmt.Errorf("response for %s exceeds 32 MiB", u.Path)
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			next, err := resp.Location()
			if err != nil {
				return fmt.Errorf("invalid redirect for %s", u.Path)
			}
			if next.Scheme != "https" || next.Host != u.Host || next.User != nil || next.Path != u.Path ||
				(method == http.MethodPost && resp.StatusCode != 307 && resp.StatusCode != 308) {
				return fmt.Errorf("refusing unexpected redirect for %s", u.Path)
			}
			u = next
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("Ozon Tech HTTP %d for %s; refresh Chrome and run auth import", resp.StatusCode, u.Path)
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
			return fmt.Errorf("Ozon Tech returned a non-JSON response for %s, possibly an antibot challenge; open the site in Chrome, then auth import", u.Path)
		}
		if err := json.Unmarshal(b, result); err != nil {
			return fmt.Errorf("invalid JSON response for %s", u.Path)
		}
		return nil
	}
	return fmt.Errorf("Ozon Tech redirect loop for %s; open the site in Chrome, then auth import", u.Path)
}
