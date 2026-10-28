package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// CloneMetadata comes from a paused native Duplicate request. Authentication
// still uses the selected Chrome account; metadata contains no cookies/tokens.
type CloneMetadata struct {
	UserPrint  map[string]string `json:"user_print"`
	UserAgent  string            `json:"user_agent"`
	Origin     string            `json:"origin"`
	CapturedAt time.Time         `json:"captured_at"`
}

type DuplicatePlan struct {
	SourceID              string   `json:"source_id"`
	SourceViewURL         string   `json:"source_view_url"`
	Applied               bool     `json:"applied"`
	Verified              bool     `json:"verified"`
	ID                    string   `json:"id,omitempty"`
	ViewURL               string   `json:"view_url,omitempty"`
	NativeURL             string   `json:"native_url,omitempty"`
	AccessType            any      `json:"access_type"`
	Status                any      `json:"status,omitempty"`
	BackupPath            string   `json:"backup_path,omitempty"`
	OtherResumesUnchanged []string `json:"other_resumes_unchanged,omitempty"`
}

// Duplicate uses the native adapter observed on 2026-10-28: POST {hash,userPrint}
// and a {url} success response. It never calls publication/application endpoints.
func (c *Client) Duplicate(ctx context.Context, source ResumeID, metadata CloneMetadata, apply bool, backupDir string) (*DuplicatePlan, error) {
	if err := c.validateBrowserMetadata(metadata); err != nil {
		return nil, err
	}
	return c.duplicate(ctx, source, metadata, apply, backupDir)
}

func (c *Client) validateBrowserMetadata(metadata CloneMetadata) error {
	if metadata.Origin != c.base.String() || metadata.UserAgent == "" || strings.ContainsAny(metadata.UserAgent, "\r\n") {
		return fmt.Errorf("invalid browser metadata: origin must match %s and user_agent must be captured from that browser", c.base)
	}
	if metadata.CapturedAt.IsZero() || time.Since(metadata.CapturedAt) > 24*time.Hour || time.Until(metadata.CapturedAt) > time.Minute {
		return fmt.Errorf("browser metadata is missing or older than 24 hours; capture the native request again")
	}
	if len(metadata.UserPrint) != 2 || metadata.UserPrint["fingerprintIteration2"] == "" || metadata.UserPrint["fingerprintSp"] == "" {
		return fmt.Errorf("invalid user_print: use both fingerprint fields from the native browser request; do not invent values")
	}
	return nil
}

func (c *Client) duplicate(ctx context.Context, source ResumeID, metadata CloneMetadata, apply bool, backupDir string) (*DuplicatePlan, error) {
	list, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := ResumeIDs(list)
	if err != nil {
		return nil, err
	}
	before := make(map[string]Object, len(ids))
	var selected *Resume
	for _, id := range ids {
		resume, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		before[id.String()] = isolatedState(resume.Fields)
		if id == source {
			selected = resume
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("clone source %s is not in the selected account; no write attempted", source)
	}
	attrs, _ := selected.Fields["_attributes"].(map[string]any)
	if attrs["permission"] != "edit" {
		return nil, fmt.Errorf("clone source %s is not editable; no write attempted", source)
	}
	view, err := c.View(ctx, source)
	if err != nil {
		return nil, err
	}
	plan := &DuplicatePlan{SourceID: source.String(), SourceViewURL: view.URL, AccessType: Editable(selected.Fields)["accessType"]}
	if !apply {
		return plan, nil
	}
	if backupDir == "" {
		return nil, fmt.Errorf("clone backup directory is empty; no write attempted")
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}
	backup, err := os.CreateTemp(filepath.Clean(backupDir), source.String()+"-clone-*.json")
	if err != nil {
		return nil, err
	}
	plan.BackupPath = backup.Name()
	encodeErr := json.NewEncoder(backup).Encode(Object{"source": selected, "resumes": before})
	closeErr := backup.Close()
	if encodeErr != nil {
		return nil, encodeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	response, err := c.cloneRequest(ctx, source, metadata)
	if err != nil {
		return nil, fmt.Errorf("%w; inspect resumes list before retrying; backup: %s", err, plan.BackupPath)
	}
	plan.Applied = true
	nativeURL, _ := response["url"].(string)
	parsed, err := url.Parse(nativeURL)
	if err != nil || nativeURL == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Host != "" && (!IsHHHost(parsed.Hostname()) || parsed.Port() != "") || parsed.IsAbs() && parsed.Scheme != "https" {
		return nil, fmt.Errorf("clone returned no valid HH resume URL; outcome uncertain, inspect resumes list; backup: %s", plan.BackupPath)
	}
	plan.NativeURL = nativeURL
	afterList, err := c.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("clone returned %s but list verification failed: %w; do not retry", nativeURL, err)
	}
	afterIDs, err := ResumeIDs(afterList)
	if err != nil {
		return nil, err
	}
	var added []ResumeID
	seen := map[string]bool{}
	for _, id := range afterIDs {
		seen[id.String()] = true
		if _, exists := before[id.String()]; !exists {
			added = append(added, id)
		}
	}
	if len(added) != 1 || len(afterIDs) != len(ids)+1 || !strings.Contains(nativeURL, added[0].String()) {
		return nil, fmt.Errorf("clone result %s cannot be bound to exactly one new public resume ID; inspect account before retrying", nativeURL)
	}
	for _, id := range ids {
		if !seen[id.String()] {
			return nil, fmt.Errorf("existing resume %s disappeared during clone; stop", id)
		}
		current, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(before[id.String()], isolatedState(current.Fields)) {
			return nil, fmt.Errorf("ISOLATION FAILURE: resume %s changed during clone; stop; backup: %s", id, plan.BackupPath)
		}
		plan.OtherResumesUnchanged = append(plan.OtherResumesUnchanged, id.String())
	}
	created, err := c.Get(ctx, added[0])
	if err != nil {
		return nil, err
	}
	createdView, err := c.View(ctx, added[0])
	if err != nil {
		return nil, err
	}
	// Report the clone's actual status/visibility without silently publishing it.
	createdAttrs, _ := created.Fields["_attributes"].(map[string]any)
	plan.ID, plan.ViewURL = created.ID, createdView.URL
	plan.AccessType = Editable(created.Fields)["accessType"]
	plan.Status = createdAttrs["status"]
	plan.Verified = true
	sort.Strings(plan.OtherResumesUnchanged)
	return plan, nil
}

func (c *Client) cloneRequest(ctx context.Context, source ResumeID, metadata CloneMetadata) (Object, error) {
	endpoint, _ := url.Parse("https://resume-profile-front.hh.ru/profile/shards/resume/clone")
	return c.nativeResumeRequest(ctx, endpoint, Object{"hash": source.String(), "userPrint": metadata.UserPrint}, metadata)
}

func (c *Client) nativeResumeRequest(ctx context.Context, endpoint *url.URL, fields Object, metadata CloneMetadata) (Object, error) {
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", metadata.Origin)
	req.Header.Set("Referer", metadata.Origin+"/applicant/profile/me")
	req.Header.Set("User-Agent", metadata.UserAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if c.http.Jar != nil {
		for _, cookie := range c.http.Jar.Cookies(req.URL) {
			if cookie.Name == "_xsrf" {
				req.Header.Set("X-Xsrftoken", cookie.Value)
			}
		}
	}
	if req.Header.Get("X-Xsrftoken") == "" {
		return nil, fmt.Errorf("missing _xsrf cookie for native resume host; no write attempted")
	}
	started := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("native resume request: %w; outcome may be uncertain, no automatic retry", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read native resume response: %w; outcome may be uncertain", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, &HTTPError{Method: http.MethodPost, URL: endpoint.String(), Status: res.StatusCode, DurationMS: time.Since(started).Milliseconds(), RetryAfter: res.Header.Get("Retry-After"), RequestKeys: keys, Response: boundedResponse(body)}
	}
	var result Object
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode native resume response: %w; inspect before retrying", err)
	}
	if problem, ok := result["error"]; ok && problem != nil && problem != "" {
		return nil, fmt.Errorf("HH rejected native resume request: %s", boundedResponse(body))
	}
	return result, nil
}
