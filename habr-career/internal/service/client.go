package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"golang.org/x/net/html"
)

type Client struct{ http *http.Client }

func NewClient(client *http.Client) *Client {
	owned := *client
	owned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &owned}
}

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

func (c *Client) request(ctx context.Context, method, path, csrf string, fields map[string]string) ([]byte, error) {
	var body bytes.Buffer
	contentType := ""
	var keys []string
	if method != http.MethodGet {
		if csrf == "" {
			return nil, fmt.Errorf("missing native CSRF token for %s %s; no write attempted", method, path)
		}
		writer := multipart.NewWriter(&body)
		for key, value := range fields {
			keys = append(keys, key)
			if err := writer.WriteField(key, value); err != nil {
				return nil, err
			}
		}
		if len(fields) == 0 {
			// Chrome serializes empty FormData without a MIME preamble. Go's
			// Writer.Close adds a leading CRLF, which this native route rejects.
			fmt.Fprintf(&body, "--%s--\r\n", writer.Boundary())
		} else {
			if err := writer.Close(); err != nil {
				return nil, err
			}
		}
		contentType = writer.FormDataContentType()
	}
	return c.requestBody(ctx, method, path, csrf, &body, contentType, keys, false)
}

func (c *Client) requestForm(ctx context.Context, path, csrf string, fields url.Values) ([]byte, error) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	return c.requestBody(ctx, http.MethodPost, path, csrf, strings.NewReader(fields.Encode()), "application/x-www-form-urlencoded; charset=UTF-8", keys, true)
}

func (c *Client) requestBody(ctx context.Context, method, path, csrf string, body io.Reader, contentType string, keys []string, script bool) ([]byte, error) {
	u, err := url.Parse(Origin + path)
	if err != nil || u.Scheme != "https" || u.Host != "career.habr.com" || u.User != nil {
		return nil, fmt.Errorf("invalid request path %q; expected a same-origin Habr Career path", path)
	}
	if method != http.MethodGet && csrf == "" {
		return nil, fmt.Errorf("missing native CSRF token for %s %s; no write attempted", method, path)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36")
	if strings.HasPrefix(path, "/api/") {
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "text/html")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Origin", Origin)
		req.Header.Set("Referer", Origin+"/vacancies/"+strings.Split(strings.TrimPrefix(path, "/api/frontend/vacancies/"), "/")[0])
	}
	if script {
		req.Header.Set("Accept", "*/*;q=0.5, text/javascript, application/javascript, application/ecmascript, application/x-ecmascript")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Referer", Origin+path+"/edit")
	}
	started := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w; no automatic retries; inspect destination state before retrying a write", method, u.Redacted(), err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, fmt.Errorf("incomplete or oversized response to %s %s; outcome may be uncertain, inspect state before retrying", method, path)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		sort.Strings(keys)
		diagnostic := "non-JSON response; stop on authentication/protection challenges"
		var value any
		if json.Unmarshal(data, &value) == nil {
			b, _ := json.Marshal(redact(value))
			if csrf != "" {
				b = bytes.ReplaceAll(b, []byte(csrf), []byte("[redacted]"))
			}
			for _, cookie := range req.Cookies() {
				if cookie.Value != "" {
					b = bytes.ReplaceAll(b, []byte(cookie.Value), []byte("[redacted]"))
				}
			}
			if len(b) > 1024 {
				b = append(b[:1024], []byte("…[truncated]")...)
			}
			diagnostic = string(b)
		}
		return nil, &HTTPError{Method: method, URL: u.Redacted(), Status: res.StatusCode, DurationMS: time.Since(started).Milliseconds(), RetryAfter: res.Header.Get("Retry-After"), RequestKeys: keys, Response: diagnostic}
	}
	return data, nil
}

func (c *Client) readPage(ctx context.Context, path string) (*page, error) {
	body, err := c.request(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	return decodePage(body)
}

func (c *Client) Profile(ctx context.Context) (Object, error) {
	body, err := c.request(ctx, http.MethodGet, "/api/frontend_v1/users/me", "", nil)
	if err != nil {
		return nil, err
	}
	var data Object
	if json.Unmarshal(body, &data) != nil {
		return nil, fmt.Errorf("account endpoint did not return JSON; authentication or request contract changed")
	}
	user := object(data["user"])
	alias, _ := user["alias"].(string)
	if _, err := domain.NewResumeIDFromValue(alias); err != nil {
		return nil, fmt.Errorf("account endpoint has no confirmed signed-in alias; run auth import after signing in")
	}
	return redact(user).(map[string]any), nil
}

func (c *Client) Resumes(ctx context.Context) (Object, error) {
	profile, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	id := profile["alias"].(string)
	return Object{"resumes": []any{Object{"id": id, "name": profile["fullName"], "email": profile["email"], "url": Origin + "/" + id}}}, nil
}

type Resume struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	ViewURL string `json:"view_url"`
	User    Object `json:"user"`
	Fields  any    `json:"fields"`
	Text    string `json:"text"`
}

func (c *Client) Resume(ctx context.Context, id domain.ResumeID) (*Resume, error) {
	if _, err := domain.NewResumeIDFromValue(id.String()); err != nil {
		return nil, err
	}
	p, err := c.readPage(ctx, "/"+id.String())
	if err != nil {
		return nil, err
	}
	user := object(p.state["user"])
	if user["id"] != id.String() || user["isOwner"] != true || user["shouldDisplayInfo"] != true || p.state["placeholder"] != nil {
		return nil, fmt.Errorf("resume %q is not the confirmed visible signed-in account profile; stopped", id.String())
	}
	profileURL := Origin + "/" + id.String()
	if object(user["print"])["html"] != profileURL+"/print" {
		return nil, fmt.Errorf("resume %q print URL does not identify the selected native profile; stopped", id.String())
	}
	body, err := c.request(ctx, http.MethodGet, "/"+id.String()+"/print", "", nil)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	main := find(doc, func(n *html.Node) bool { return n.Data == "div" && attr(n, "class") == "content" })
	identity := find(doc, func(n *html.Node) bool { return n.Data == "a" && attr(n, "href") == profileURL })
	name := find(doc, func(n *html.Node) bool { return n.Data == "h1" && attr(n, "class") == "full_name" })
	if main == nil || identity == nil || name == nil || nodeText(name) != user["title"] {
		return nil, fmt.Errorf("resume %q employer print-view identity/content could not be verified; stopped", id.String())
	}
	return &Resume{ID: id.String(), URL: profileURL, ViewURL: profileURL + "/print", User: redact(user).(map[string]any), Fields: redact(p.state["resume"]), Text: nodeText(main)}, nil
}

type Vacancy struct {
	ID              string `json:"id"`
	URL             string `json:"url"`
	Title           string `json:"title"`
	Employer        Object `json:"employer"`
	Salary          any    `json:"salary"`
	PredictedSalary any    `json:"predicted_salary,omitempty"`
	Location        any    `json:"location"`
	Remote          bool   `json:"remote"`
	Employment      any    `json:"employment"`
	Qualification   any    `json:"qualification"`
	Skills          any    `json:"skills"`
	Description     string `json:"description"`
	Archived        bool   `json:"archived"`
	Hidden          bool   `json:"hidden"`
	ResponseKind    string `json:"response_kind"`
	ExternalLink    any    `json:"external_link"`
	UserAlias       string `json:"user_alias"`
	Response        Object `json:"response"`
	Placeholder     any    `json:"placeholder"`
	Fields          Object `json:"fields"`
	ResponsesLeft   any    `json:"responses_left"`
	csrf            string
}

func (c *Client) Vacancy(ctx context.Context, id domain.VacancyID) (*Vacancy, error) {
	if _, err := domain.NewVacancyIDFromValue(id.String()); err != nil {
		return nil, err
	}
	p, err := c.readPage(ctx, "/vacancies/"+id.String())
	if err != nil {
		return nil, err
	}
	v := object(p.state["vacancy"])
	form := object(p.state["createResponse"])
	visitor := object(p.state["visitor"])
	alias, _ := p.state["userLogin"].(string)
	if number(v["id"]) != id.String() || v["href"] != "/vacancies/"+id.String() || number(form["vacancyId"]) != id.String() || alias == "" || visitor["login"] != alias {
		return nil, fmt.Errorf("vacancy %s identity or signed-in native response form could not be verified; stopped", id.String())
	}
	archived, archivedKnown := v["archived"].(bool)
	hidden, hiddenKnown := v["hidden"].(bool)
	title, _ := v["title"].(string)
	description, _ := v["description"].(string)
	company := object(v["company"])
	kind, _ := object(v["response"])["kind"].(string)
	if !archivedKnown || !hiddenKnown || title == "" || description == "" || number(company["id"]) == "" || company["title"] == nil {
		return nil, fmt.Errorf("vacancy %s has incomplete role/company/status evidence; stopped", id.String())
	}
	return &Vacancy{
		ID: id.String(), URL: Origin + "/vacancies/" + id.String(), Title: title, Employer: company,
		Salary: v["salary"], PredictedSalary: v["predictedSalary"], Location: v["locations"], Remote: v["remoteWork"] == true,
		Employment: v["employment"], Qualification: v["qualification"], Skills: v["skills"], Description: htmlText(description),
		Archived: archived, Hidden: hidden, ResponseKind: kind, ExternalLink: v["externalLink"], UserAlias: alias,
		Response: object(form["response"]), Placeholder: p.state["placeholder"], Fields: object(form["fields"]), ResponsesLeft: form["responsesLeft"], csrf: p.csrf,
	}, nil
}

func (c *Client) Application(ctx context.Context, vacancyID domain.VacancyID, resumeID domain.ResumeID) (Object, error) {
	v, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return nil, err
	}
	if v.UserAlias != resumeID.String() {
		return nil, fmt.Errorf("selected resume %q differs from signed-in profile %q; stopped", resumeID.String(), v.UserAlias)
	}
	out := Object{"vacancy_id": v.ID, "vacancy_url": v.URL, "company": v.Employer["title"], "title": v.Title, "resume_id": resumeID.String(), "submitted": v.Response != nil}
	if v.Response == nil {
		return out, nil
	}
	if number(v.Response["id"]) == "" || object(v.Response["author"])["id"] != resumeID.String() {
		return nil, fmt.Errorf("vacancy %s response identity does not confirm resume %q; stopped", v.ID, resumeID.String())
	}
	out["response_id"] = number(v.Response["id"])
	summary := Object{}
	for _, key := range []string{"id", "canUpdate", "canDelete", "createdAfterAuth", "isQuick", "message", "publishedAt", "result"} {
		if value, ok := v.Response[key]; ok {
			summary[key] = value
		}
	}
	author := object(v.Response["author"])
	summary["author"] = Object{"id": author["id"], "title": author["title"], "href": author["href"]}
	out["response"] = summary
	out["letter"] = v.Response["message"]
	return out, nil
}
