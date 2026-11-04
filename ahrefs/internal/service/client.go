package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const maxResponseBytes = 32 << 20

var diagnosticPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/ -]{1,128}$`)

type Client struct {
	http *http.Client
}

func NewClient(client *http.Client) *Client { return &Client{http: client} }

// All endpoints used here are the web application's read-only POST queries.
func (c *Client) call(ctx context.Context, endpoint string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", endpoint, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Origin+"/v4/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create %s request: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", Origin)
	req.Header.Set("Referer", Origin+"/site-audit")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request to Ahrefs %s failed: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		extra := ""
		if retry := resp.Header.Get("Retry-After"); resp.StatusCode == http.StatusTooManyRequests && diagnosticPattern.MatchString(retry) {
			extra += "; Retry-After: " + retry
		}
		if requestID := resp.Header.Get("X-Request-Id"); diagnosticPattern.MatchString(requestID) {
			extra += "; request ID: " + requestID
		}
		return fmt.Errorf("request to Ahrefs %s returned HTTP %d%s; no retry performed; check the signed-in Chrome session before rerunning", endpoint, resp.StatusCode, extra)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read Ahrefs %s response: %w", endpoint, err)
	}
	if len(b) > maxResponseBytes {
		return fmt.Errorf("response from Ahrefs %s exceeded %d bytes", endpoint, maxResponseBytes)
	}
	if !json.Valid(b) {
		return fmt.Errorf("request to Ahrefs %s returned a non-JSON response; check for a login or protection page in Chrome", endpoint)
	}
	// Some web queries return bare objects/arrays, others an OCaml result tuple.
	if strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
		var parts []json.RawMessage
		if err := json.Unmarshal(b, &parts); err != nil {
			return fmt.Errorf("decode Ahrefs %s response: %w", endpoint, err)
		}
		var tag string
		if len(parts) > 0 && json.Unmarshal(parts[0], &tag) == nil {
			if tag != "Ok" || len(parts) != 2 {
				return fmt.Errorf("request to Ahrefs %s returned an API error or unsupported result; check the signed-in Chrome session", endpoint)
			}
			b = parts[1]
		}
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return fmt.Errorf("request to Ahrefs %s returned an empty result", endpoint)
	}
	if err := json.Unmarshal(b, output); err != nil {
		return fmt.Errorf("decode Ahrefs %s response: %w", endpoint, err)
	}
	return nil
}
