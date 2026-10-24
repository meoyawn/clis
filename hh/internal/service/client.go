package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Client struct {
	http *http.Client
	base *url.URL
}

func NewClient(client *http.Client, base *url.URL) *Client {
	return &Client{http: client, base: base}
}

// HTTPError retains bounded, redacted server diagnostics on stderr, not stdout.
type HTTPError struct {
	Method      string   `json:"method"`
	URL         string   `json:"url"`
	Status      int      `json:"status"`
	DurationMS  int64    `json:"duration_ms"`
	RetryAfter  string   `json:"retry_after,omitempty"`
	RequestKeys []string `json:"request_keys,omitempty"`
	Response    string   `json:"response"`
}

func (e *HTTPError) Error() string {
	b, _ := json.Marshal(e)
	return string(b)
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, payload Object, htmlPage bool) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode %s payload: %w", path, err)
		}
		body = bytes.NewReader(data)
	}
	return c.requestBody(ctx, method, path, query, payload, htmlPage, body, "application/json")
}

func (c *Client) requestBody(ctx context.Context, method, path string, query url.Values, payload Object, htmlPage bool, body io.Reader, contentType string) ([]byte, error) {
	u := c.base.ResolveReference(&url.URL{Path: path, RawQuery: query.Encode()})
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", method, err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	if htmlPage {
		req.Header.Set("Accept", "text/html")
	} else {
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	if payload != nil {
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Origin", c.base.String())
		req.Header.Set("Referer", c.base.String()+"/applicant/profile/me")
		if c.http.Jar != nil {
			for _, cookie := range c.http.Jar.Cookies(u) {
				if cookie.Name == "_xsrf" {
					req.Header.Set("X-Xsrftoken", cookie.Value)
				}
			}
		}
		if req.Header.Get("X-Xsrftoken") == "" {
			return nil, fmt.Errorf("missing _xsrf cookie for %s: read the selected resume first; no write attempted", u.Host)
		}
	}
	started := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s after %s: %w; writes are never retried automatically", method, u.Redacted(), time.Since(started).Round(time.Millisecond), err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s %s: %w; outcome may be uncertain, inspect before retrying", method, path, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || strings.Contains(res.Request.URL.Path, "/account/login") || strings.Contains(res.Request.URL.Path, "/account/signup") {
		keys := make([]string, 0, len(payload))
		for key := range payload {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, &HTTPError{Method: method, URL: u.Redacted(), Status: res.StatusCode, DurationMS: time.Since(started).Milliseconds(), RetryAfter: res.Header.Get("Retry-After"), RequestKeys: keys, Response: boundedResponse(data)}
	}
	return data, nil
}

func boundedResponse(data []byte) string {
	var v any
	if json.Unmarshal(data, &v) == nil {
		data, _ = json.Marshal(Redact(v))
	}
	if len(data) > 2048 {
		data = append(data[:2048], []byte("…[truncated]")...)
	}
	return string(data)
}

func Redact(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(Object, len(v))
		for key, val := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "cookie") || strings.Contains(lower, "password") || strings.Contains(lower, "xsrf") || strings.Contains(lower, "csrf") || lower == "authorization" || lower == "otp" {
				out[key] = "[redacted]"
			} else {
				out[key] = Redact(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = Redact(x)
		}
		return out
	default:
		return value
	}
}

func (c *Client) List(ctx context.Context) (Object, error) {
	body, err := c.request(ctx, http.MethodGet, "/shards/applicant/resumes", nil, nil, false)
	if err != nil {
		return nil, err
	}
	var out Object
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode resume list: %w (authentication or API changed)", err)
	}
	if _, ok := out["resumes"]; !ok {
		return nil, fmt.Errorf("resume list response has no resumes field: %s", boundedResponse(body))
	}
	return out, nil
}

// Profile reads the shared account profile through the recorded profile adapter.
// It is intentionally separate from resume-scoped operations.
func (c *Client) Profile(ctx context.Context) (Object, error) {
	body, err := c.request(ctx, http.MethodGet, "/shards/applicant/profile/get_full_data", nil, nil, false)
	if err != nil {
		return nil, err
	}
	var out Object
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode shared profile: %w", err)
	}
	return out, nil
}

type Resume struct {
	ID      string `json:"id"`
	ViewURL string `json:"view_url"`
	EditURL string `json:"edit_url"`
	Fields  Object `json:"fields"`
}

func (c *Client) URLs(id ResumeID) (string, string) {
	return c.base.String() + "/resume/" + id.String() + "?print=true", c.base.String() + "/resume/" + id.String()
}

func (c *Client) Get(ctx context.Context, id ResumeID) (*Resume, error) {
	body, err := c.request(ctx, http.MethodGet, "/applicant/resume", url.Values{"resume": {id.String()}}, nil, false)
	if err != nil {
		return nil, err
	}
	state, err := DecodeState(body)
	if err != nil {
		return nil, err
	}
	fields, ok := state["applicantResume"].(map[string]any)
	if !ok {
		fields, ok = state["resume"].(map[string]any)
	}
	if !ok {
		if _, present := state["_attributes"]; present {
			fields, ok = state, true
		}
	}
	if !ok {
		return nil, fmt.Errorf("resume %s response has no applicantResume; authentication or API changed", id)
	}
	attrs, _ := fields["_attributes"].(map[string]any)
	if attrs["hash"] != id.String() {
		return nil, fmt.Errorf("resume identity mismatch: requested %s, server returned %v; stopped", id, attrs["hash"])
	}
	view, edit := c.URLs(id)
	return &Resume{ID: id.String(), ViewURL: view, EditURL: edit, Fields: fields}, nil
}

type View struct {
	ID                string        `json:"id"`
	URL               string        `json:"url"`
	Verified          bool          `json:"verified"`
	VerificationScope string        `json:"verification_scope"`
	Text              string        `json:"text"`
	Contacts          PrintContacts `json:"contacts"`
	HTML              []byte        `json:"-"`
	Fields            Object        `json:"-"`
}

func (c *Client) View(ctx context.Context, id ResumeID) (*View, error) {
	body, err := c.request(ctx, http.MethodGet, "/resume/"+id.String(), url.Values{"print": {"true"}}, nil, true)
	if err != nil {
		return nil, err
	}
	state, err := DecodeState(body)
	if err != nil {
		return nil, err
	}
	resume, _ := state["applicantResume"].(map[string]any)
	if resume == nil {
		resume, _ = state["resume"].(map[string]any)
	}
	attrs, _ := resume["_attributes"].(map[string]any)
	returnedID := attrs["hash"]
	if returnedID == nil {
		returnedID = resume["hash"]
	}
	if returnedID != id.String() {
		keys := make([]string, 0, len(state))
		for key := range state {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("print view identity mismatch for %s: got %v; state keys: %v", id, returnedID, keys)
	}
	text, err := PrintText(body)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, fmt.Errorf("print view %s is empty; cannot verify", id)
	}
	u, _ := c.URLs(id)
	fields := make(Object)
	for key, value := range resume {
		if !stringFields[key] && !objectFields[key] {
			continue
		}
		wrapped, ok := value.(map[string]any)
		if !ok {
			// Unlike the other editable print fields, hiddenFields is an
			// unwrapped array of scalar visibility flags.
			if key == "hiddenFields" {
				if _, ok := value.([]any); ok {
					fields[key] = value
				}
			}
			continue
		}
		value = wrapped["value"]
		if value == nil {
			fields[key] = []any{}
			continue
		}
		if _, ok := value.([]any); ok {
			fields[key] = value
			continue
		}
		if stringFields[key] {
			fields[key] = []any{Object{"string": value}}
		} else {
			fields[key] = []any{value}
		}
	}
	contacts, err := ReadPrintContacts(body)
	if err != nil {
		return nil, err
	}
	return &View{ID: id.String(), URL: u, Verified: true, VerificationScope: "page identity and rendered content; inspect contacts for semantic correctness", Text: text, Contacts: contacts, HTML: body, Fields: fields}, nil
}
