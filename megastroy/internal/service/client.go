package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/publicsuffix"
)

const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
const maxPageBytes = 16 << 20

// DDoSGuardError identifies a blocked catalog request that needs human clearance.
type DDoSGuardError struct {
	URL string
}

func (e *DDoSGuardError) Error() string {
	return fmt.Sprintf("DDoS-Guard rejected the anonymous session at %s; no request was retried", e.URL)
}

type Client struct {
	http     *http.Client
	base     *url.URL
	session  Session
	requests int
}

func NewClient(session Session) (*Client, error) {
	base, err := url.Parse(Origin)
	if err != nil {
		return nil, fmt.Errorf("parse catalog origin: %w", err)
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	jar.SetCookies(base, session.HTTPCookies(time.Now()))
	if session.UserAgent == "" {
		session.UserAgent = DefaultUserAgent
	}
	if strings.ContainsAny(session.UserAgent, "\r\n") {
		return nil, fmt.Errorf("user-agent must not contain newlines")
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		DisableKeepAlives:     true,
	}
	return &Client{
		base: base, session: session,
		http: &http.Client{Transport: transport, Jar: jar, Timeout: 60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) Session() Session { return c.session }

func (c *Client) do(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if c.requests >= 1 {
		return nil, fmt.Errorf("one-request budget exhausted")
	}
	req.Header.Set("User-Agent", c.session.UserAgent)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	c.requests++
	response, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", req.URL.Path, err)
	}
	return response, nil
}

func (c *Client) get(ctx context.Context, u *url.URL) (*goquery.Document, int, error) {
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host || u.User != nil {
		return nil, 0, fmt.Errorf("refusing a request outside %s", Origin)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := c.do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden {
		return nil, 0, &DDoSGuardError{URL: u.String()}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", u.Path, err)
	}
	if len(body) > maxPageBytes {
		return nil, 0, fmt.Errorf("page exceeds %d bytes", maxPageBytes)
	}
	lower := bytes.ToLower(body)
	if bytes.Contains(lower, []byte("<title>ddos-guard")) || bytes.Contains(lower, []byte("data-ddg-origin=")) {
		return nil, 0, &DDoSGuardError{URL: u.String()}
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, 0, fmt.Errorf("HTTP %d redirect from %s; redirect not followed", response.StatusCode, u.Path)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound {
		return nil, 0, fmt.Errorf("HTTP %d from %s", response.StatusCode, u.Path)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("parse HTML: %w", err)
	}
	// Only anonymous anti-bot cookies are retained. Account/session cookies from
	// the public site never become part of the CLI's saved state.
	if response.StatusCode == http.StatusOK {
		c.session.Merge(response.Cookies(), time.Now())
	}
	return doc, response.StatusCode, nil
}

type SearchOptions struct {
	Query          string
	Page           int
	Limit          int
	IncludeFilters bool
	Filters        url.Values
	Sort           Sort
}

var filterKeyPattern = regexp.MustCompile(`^(?:field[1-9][0-9]*\[\]|(?:field[1-9][0-9]*|price)\[(?:from|to)\]|category_id|in_stock)$`)
var bareFieldPattern = regexp.MustCompile(`^field[1-9][0-9]*$`)

func ParseFilters(raw []string) (url.Values, error) {
	values := url.Values{}
	for _, entry := range raw {
		key, value, ok := strings.Cut(entry, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if bareFieldPattern.MatchString(key) {
			key += "[]"
		}
		if !ok || !filterKeyPattern.MatchString(key) || value == "" {
			return nil, fmt.Errorf("invalid filter %q: use a key and value returned by search --include-filters (for example field79[]=Terminus)", entry)
		}
		if !strings.HasSuffix(key, "[]") && values.Has(key) {
			return nil, fmt.Errorf("filter %q may only be supplied once", key)
		}
		if key == "category_id" && !productIDPattern.MatchString(value) {
			return nil, fmt.Errorf("invalid category_id %q: expected a positive integer", value)
		}
		if key == "in_stock" && value != "0" && value != "1" {
			return nil, fmt.Errorf("invalid in_stock %q: expected 0 or 1", value)
		}
		if strings.HasSuffix(key, "[from]") || strings.HasSuffix(key, "[to]") {
			n := number(value)
			if n == nil || *n < 0 {
				return nil, fmt.Errorf("invalid range value %q for %s: expected a nonnegative number", value, key)
			}
			value = strconv.FormatFloat(*n, 'f', -1, 64)
		}
		if !slices.Contains(values[key], value) {
			values.Add(key, value)
		}
	}
	for key, valuesForKey := range values {
		if stem, ok := strings.CutSuffix(key, "[from]"); ok {
			if max := number(values.Get(stem + "[to]")); max != nil && *number(valuesForKey[0]) > *max {
				return nil, fmt.Errorf("invalid %s range: from %s exceeds to %s", stem, valuesForKey[0], values.Get(stem+"[to]"))
			}
		}
	}
	return values, nil
}

func (o SearchOptions) target(base *url.URL) (*url.URL, error) {
	if strings.TrimSpace(o.Query) == "" {
		return nil, fmt.Errorf("query %q must not be empty", o.Query)
	}
	if o.Page < 1 {
		return nil, fmt.Errorf("page %d must be at least 1", o.Page)
	}
	if o.Limit < 0 {
		return nil, fmt.Errorf("limit %d must not be negative", o.Limit)
	}
	var raw []string
	for k, vs := range o.Filters {
		for _, v := range vs {
			raw = append(raw, k+"="+v)
		}
	}
	values, err := ParseFilters(raw)
	if err != nil {
		return nil, err
	}
	values.Set("q", strings.TrimSpace(o.Query))
	values.Set("noredirect", "1")
	if o.Page > 1 {
		values.Set("page", strconv.Itoa(o.Page-1))
	}
	if o.Sort.value != "" {
		values.Set("sort", o.Sort.String())
	}
	return base.ResolveReference(&url.URL{Path: "/catalog/search", RawQuery: values.Encode()}), nil
}

func (c *Client) Search(ctx context.Context, o SearchOptions) (*SearchResult, error) {
	u, err := o.target(c.base)
	if err != nil {
		return nil, err
	}
	doc, status, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	result, err := parseSearch(doc, u, o, status)
	if err != nil {
		return nil, fmt.Errorf("parse search: %w", err)
	}
	result.Metadata = metadata(doc, u, c.requests)
	if o.Limit > 0 && o.Limit < len(result.Results) {
		result.Results = result.Results[:o.Limit]
		result.Count = len(result.Results)
		result.Warnings = append(result.Warnings, "--limit trims this server page; next_page skips the omitted products too")
	}
	return result, nil
}

func (c *Client) Details(ctx context.Context, rawID string) (*DetailResult, error) {
	id, err := productID(rawID)
	if err != nil {
		return nil, err
	}
	u := c.base.ResolveReference(&url.URL{Path: "/products/" + id})
	doc, status, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("product %s returned HTTP %d", id, status)
	}
	result, err := parseDetails(doc, u, id)
	if err != nil {
		return nil, fmt.Errorf("parse product %s: %w", id, err)
	}
	result.Metadata = metadata(doc, u, c.requests)
	if len(result.Characteristics) == 0 {
		result.Warnings = append(result.Warnings, "No characteristics published in the recognized product section")
	}
	return result, nil
}

func metadata(doc *goquery.Document, u *url.URL, requests int) Metadata {
	location := textOf(doc.Find(".selected-city").First())
	if location == "" {
		location = "Казань"
	}
	return Metadata{SourceURL: u.String(), FetchedAt: time.Now().UTC(), HTTPRequests: requests, Location: location}
}
