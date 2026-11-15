package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	if selected := c.headers.Get("User-Agent"); selected != "" && selected != metadata.UserAgent {
		return fmt.Errorf("native resume metadata User-Agent differs from the selected Chrome session; capture both from the same session")
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
	body, err := c.requestAt(ctx, http.MethodPost, endpoint, fields, false, bytes.NewReader(payload), "application/json", http.Header{
		"Origin":     {metadata.Origin},
		"Referer":    {metadata.Origin + "/applicant/profile/me"},
		"User-Agent": {metadata.UserAgent},
	})
	if err != nil {
		return nil, fmt.Errorf("native resume request: %w", err)
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
