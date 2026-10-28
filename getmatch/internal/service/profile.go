package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (c *Client) ProfileCV(ctx context.Context) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Origin+"/api/profiles/cv", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Accept", "application/pdf")
	req.Header.Set("Referer", Origin+"/profile")
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download profile CV: %w; not retried", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download profile CV: HTTP %d, Retry-After=%q; stopped without retry", res.StatusCode, res.Header.Get("Retry-After"))
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (10<<20)+1))
	if err != nil || len(data) > 10<<20 || !bytes.HasPrefix(data, []byte("%PDF-")) || !strings.Contains(res.Header.Get("Content-Type"), "pdf") {
		return nil, "", fmt.Errorf("profile CV response is incomplete, too large or not PDF; stopped")
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

var summaryFields = []string{"first_name", "last_name", "email", "telegram", "phone", "locations", "birthdate", "photo", "salary_expectations_currency", "salary_expectations_from", "links", "location_id", "citizenships", "work_permits", "summary", "skills"}

func summaryPayload(profile Object) Object {
	result := Object{"source": "frontend_summary"}
	for _, key := range summaryFields {
		result[key] = profile[key]
	}
	languages := []any{}
	for _, value := range profile["languages"].([]any) {
		row, _ := value.(map[string]any)
		level, _ := row["level"].(map[string]any)
		languages = append(languages, Object{"language": row["language"], "level_id": level["id"]})
	}
	result["languages"] = languages
	if result["telegram"] == nil || result["telegram"] == "" {
		delete(result, "telegram")
	}
	return result
}

func validateProfilePatch(profile, patch Object) error {
	if len(patch) == 0 {
		return fmt.Errorf("profile patch is empty")
	}
	for key, value := range patch {
		switch key {
		case "first_name", "last_name", "summary":
			if text, ok := value.(string); !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("profile patch %s requires nonempty text", key)
			}
		case "phone":
			number, ok := value.(float64)
			if !ok || number < 10000000 || number > 999999999999999 || number != float64(int64(number)) {
				return fmt.Errorf("profile phone must contain the confirmed international digits as a positive integer")
			}
		case "skills":
			rows, ok := value.([]any)
			if !ok || len(rows) > 10 {
				return fmt.Errorf("profile skills must be an array of at most 10 items")
			}
			for _, item := range rows {
				row, _ := item.(map[string]any)
				name, _ := row["name"].(string)
				if name == "" {
					return fmt.Errorf("profile skill has no name")
				}
			}
		case "links":
			rows, ok := value.([]any)
			if !ok {
				return fmt.Errorf("profile links must be an array of absolute HTTPS URLs")
			}
			for _, item := range rows {
				link, ok := item.(string)
				if !ok || !strings.HasPrefix(link, "https://") || strings.ContainsAny(link, "\r\n ") {
					return fmt.Errorf("profile links require absolute HTTPS URLs")
				}
			}
		case "languages":
			rows, ok := value.([]any)
			if !ok || len(rows) == 0 {
				return fmt.Errorf("profile languages must include the confirmed English level")
			}
			for _, item := range rows {
				row, _ := item.(map[string]any)
				level, _ := row["level_id"].(float64)
				if row["language"] == nil || level < 1 || level > 5 || level != float64(int(level)) {
					return fmt.Errorf("profile language requires language and a native level_id from 1 to 5")
				}
			}
		case "experience":
			rows, ok := value.([]any)
			if !ok || len(rows) == 0 {
				return fmt.Errorf("experience replacement must retain real employment entries")
			}
			known := map[any]bool{}
			for _, item := range profile["experience"].([]any) {
				row, _ := item.(map[string]any)
				known[row["id"]] = true
			}
			seen := map[any]bool{}
			allowed := []string{"id", "position", "summary", "from_month", "from_year", "to_month", "to_year", "skills", "company_name", "company_url", "company_id"}
			for _, item := range rows {
				row, _ := item.(map[string]any)
				id, ok := row["id"].(float64)
				if !ok || !known[id] || seen[id] {
					return fmt.Errorf("experience must use each retained current row ID exactly once; got %v", row["id"])
				}
				seen[id] = true
				for key := range row {
					if !slices.Contains(allowed, key) {
						return fmt.Errorf("unsupported experience field %q", key)
					}
				}
				if row["position"] == nil || row["summary"] == nil || row["from_year"] == nil {
					return fmt.Errorf("experience row %v lacks title, summary or start year", id)
				}
			}
		default:
			return fmt.Errorf("unsupported profile patch field %q", key)
		}
	}
	return nil
}

func (c *Client) UpdateProfile(ctx context.Context, patch Object, apply bool, backupDir string) (Object, error) {
	before, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	if err = validateProfilePatch(before, patch); err != nil {
		return nil, err
	}
	summary := summaryPayload(before)
	for key, value := range patch {
		if key != "experience" {
			summary[key] = value
		}
	}
	experience, changesExperience := patch["experience"]
	result := Object{"applied": false, "shared_profile": true, "patch": patch, "profile_id": before["id"], "before_sha256": digest(before)}
	if !apply {
		return result, nil
	}
	if backupDir == "" {
		return nil, fmt.Errorf("profile update --apply requires a private backup directory")
	}
	if err = os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(backupDir, "profile-before-"+time.Now().UTC().Format("20060102T150405")+"-*.json")
	if err != nil {
		return nil, err
	}
	data, _ := json.MarshalIndent(before, "", "  ")
	if _, err = file.Write(data); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	result["backup_path"] = filepath.Clean(file.Name())
	if len(patch) > 1 || !changesExperience {
		data, _ = json.Marshal(summary)
		if _, err = c.accountRequest(ctx, http.MethodPut, "/api/profiles/summary", "application/json", bytes.NewReader(data)); err != nil {
			return nil, fmt.Errorf("shared-profile summary update uncertain: %w; inspect profile get before retrying", err)
		}
	}
	if changesExperience {
		data, _ = json.Marshal(Object{"profile_id": before["id"], "experience": experience})
		if _, err = c.accountRequest(ctx, http.MethodPut, "/api/profiles/experience", "application/json", bytes.NewReader(data)); err != nil {
			return nil, fmt.Errorf("shared-profile experience update uncertain after summary step: %w; inspect profile get before retrying", err)
		}
	}
	after, err := c.Profile(ctx)
	if err != nil {
		return nil, fmt.Errorf("profile update returned success but readback failed: %w; do not repeat blindly", err)
	}
	actualSummary := summaryPayload(after)
	for key := range patch {
		if key != "experience" && digest(actualSummary[key]) != digest(summary[key]) {
			return nil, fmt.Errorf("saved profile %s differs from requested patch; inspect before retrying", key)
		}
	}
	if changesExperience {
		actual := []any{}
		for _, item := range after["experience"].([]any) {
			row, _ := item.(map[string]any)
			clean := Object{}
			for _, key := range []string{"id", "position", "summary", "from_month", "from_year", "to_month", "to_year", "skills", "company_name", "company_url", "company_id"} {
				clean[key] = row[key]
			}
			actual = append(actual, clean)
		}
		if digest(actual) != digest(experience) {
			return nil, fmt.Errorf("saved employment rows differ from requested patch; inspect profile get before retrying")
		}
	}
	for _, key := range append(summaryFields, "id", "educations") {
		if _, changed := patch[key]; !changed && digest(before[key]) != digest(after[key]) {
			return nil, fmt.Errorf("unrequested shared-profile field %s changed; inspect backup and readback", key)
		}
	}
	result["applied"], result["verified"], result["after_sha256"] = true, true, digest(after)
	return result, nil
}
