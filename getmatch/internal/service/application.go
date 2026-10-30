package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/meoyawn/clis/getmatch/internal/domain"
)

type Object = map[string]any

// Account requests never retry. Error bodies can contain personal data and are
// intentionally excluded from diagnostics, except for a short API error code.
func (c *Client) accountRequest(ctx context.Context, method, path, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, Origin+path, body)
	if err != nil {
		return nil, err
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", Origin+"/profile")
	if method != http.MethodGet {
		req.Header.Set("Origin", Origin)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	httpClient := *c.http
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %w; not retried; inspect state before any write retry", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return nil, fmt.Errorf("%s %s response incomplete or too large; inspect state before retrying", method, path)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var detail struct {
			Detail struct {
				Code string `json:"error_code"`
			} `json:"detail"`
		}
		_ = json.Unmarshal(data, &detail)
		code := detail.Detail.Code
		if len(code) > 80 || strings.ContainsAny(code, "\r\n <>@") {
			code = ""
		}
		return nil, fmt.Errorf("%s %s: HTTP %d, error_code=%q, Retry-After=%q; stopped without retry", method, path, res.StatusCode, code, res.Header.Get("Retry-After"))
	}
	if method != http.MethodGet && len(data) == 0 {
		return []byte("{}"), nil
	}
	if !strings.Contains(res.Header.Get("Content-Type"), "application/json") || !json.Valid(data) {
		return nil, fmt.Errorf("%s %s returned non-JSON; authentication/access or contract changed; inspect state before retrying", method, path)
	}
	return data, nil
}

func (c *Client) accountGet(ctx context.Context, path string) (Object, error) {
	data, err := c.accountRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	var result Object
	if err = json.Unmarshal(data, &result); err != nil || result == nil {
		return nil, fmt.Errorf("GET %s returned an unexpected object; stopped", path)
	}
	return result, nil
}

func (c *Client) Profile(ctx context.Context) (Object, error) {
	p, err := c.accountGet(ctx, "/api/profiles")
	if err != nil {
		return nil, err
	}
	if _, ok := p["id"].(float64); !ok || p["first_name"] == nil || p["experience"] == nil {
		return nil, fmt.Errorf("profile identity or schema missing; stopped")
	}
	return p, nil
}

func (c *Client) Vacancy(ctx context.Context, id domain.VacancyID) (Object, error) {
	if _, err := domain.NewVacancyIDFromValue(id.String()); err != nil {
		return nil, err
	}
	v, err := c.accountGet(ctx, "/api/offers/"+id.String())
	if err != nil {
		return nil, err
	}
	if fmt.Sprint(v["id"]) != id.String() || v["position"] == nil || v["company"] == nil || v["is_active"] == nil {
		return nil, fmt.Errorf("vacancy %s identity/schema differs; stopped", id.String())
	}
	return v, nil
}

func (c *Client) ApplicationState(ctx context.Context, id domain.VacancyID) (Object, error) {
	if _, err := domain.NewVacancyIDFromValue(id.String()); err != nil {
		return nil, err
	}
	state, err := c.accountGet(ctx, "/api/auth/me?vacancy_id="+id.String()+"&check_digest=true")
	if err != nil {
		return nil, err
	}
	if _, ok := state["already_applied"].(bool); !ok || state["id"] == nil {
		return nil, fmt.Errorf("authenticated application state for vacancy %s missing; stopped", id.String())
	}
	// Authentication metadata deliberately excludes email, private salary and
	// all unrelated account fields from status output.
	return Object{"candidate_id": state["id"], "already_applied": state["already_applied"], "verified_profile": state["verified"], "profile_verification_status": state["verification_status"]}, nil
}

func (c *Client) Applications(ctx context.Context, offset int) (Object, error) {
	if offset < 0 {
		return nil, fmt.Errorf("invalid application offset %d", offset)
	}
	result, err := c.accountGet(ctx, "/api/applications/candidate?section=all&offset="+strconv.Itoa(offset)+"&limit=20")
	if err != nil {
		return nil, err
	}
	if _, ok := result["applications"].([]any); !ok {
		return nil, fmt.Errorf("application list schema changed; stopped")
	}
	return result, nil
}

func (c *Client) AllApplications(ctx context.Context) (Object, error) {
	all := []any{}
	for offset := 0; offset < 10000; {
		page, err := c.Applications(ctx, offset)
		if err != nil {
			return nil, err
		}
		rows := page["applications"].([]any)
		meta, _ := page["meta"].(map[string]any)
		total, totalOK := meta["total"].(float64)
		actualOffset, offsetOK := meta["offset"].(float64)
		if !totalOK || !offsetOK || actualOffset != float64(offset) || total < float64(offset+len(rows)) || total != float64(int(total)) {
			return nil, fmt.Errorf("application pagination metadata changed at offset %d; stopped", offset)
		}
		all = append(all, rows...)
		offset += len(rows)
		if float64(offset) >= total {
			return Object{"applications": all, "total": int(total), "complete": true}, nil
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("application pagination ended before total; stopped at %d of %.0f", offset, total)
		}
	}
	return nil, fmt.Errorf("application pagination exceeds 10000 records; coverage incomplete")
}

func (c *Client) Application(ctx context.Context, id domain.VacancyID) (Object, error) {
	state, err := c.ApplicationState(ctx, id)
	if err != nil {
		return nil, err
	}
	list, err := c.AllApplications(ctx)
	if err != nil {
		return nil, err
	}
	matches := []any{}
	for _, item := range list["applications"].([]any) {
		row, _ := item.(map[string]any)
		vacancy, _ := row["vacancy"].(map[string]any)
		path, _ := vacancy["url"].(string)
		roleID, parseErr := domain.NewVacancyIDFromValue(Origin + path)
		if parseErr == nil && roleID == id {
			matches = append(matches, row)
		}
	}
	return Object{"vacancy_id": id.String(), "state": state, "applications": matches, "list_complete": true}, nil
}

type ApplicationAnswers struct {
	FirstName      string   `json:"first_name"`
	LastName       string   `json:"last_name"`
	SalaryFrom     int      `json:"salary_from"`
	SalaryCurrency string   `json:"salary_currency"`
	LocationID     string   `json:"location_id"`
	Citizenships   []string `json:"citizenships,omitempty"`
	WorkPermits    []string `json:"work_permits,omitempty"`
}

type ApplicationDraft struct {
	VacancyID     string             `json:"vacancy_id"`
	VacancyURL    string             `json:"vacancy_url"`
	Company       string             `json:"company"`
	Position      string             `json:"position"`
	VacancySHA256 string             `json:"vacancy_sha256"`
	CandidateID   string             `json:"candidate_id"`
	ProfileSHA256 string             `json:"profile_sha256"`
	CVPath        string             `json:"cv_path"`
	CVSHA256      string             `json:"cv_sha256"`
	CVBytes       int                `json:"cv_bytes"`
	CoverLetter   string             `json:"cover_letter"`
	Answers       ApplicationAnswers `json:"answers"`
	ReviewSHA256  string             `json:"review_sha256"`
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func draftDigest(d ApplicationDraft) string {
	d.ReviewSHA256 = ""
	return digest(d)
}

func (c *Client) PrepareApplication(ctx context.Context, id domain.VacancyID, cvPath, letter string, answers ApplicationAnswers) (*ApplicationDraft, error) {
	if cvPath != "" {
		return nil, fmt.Errorf("PDF uploads are disabled: Getmatch reparses them into the shared profile and can mistake projects for employment; use the reviewed native profile")
	}
	if !utf8.ValidString(letter) || strings.TrimSpace(letter) == "" || utf8.RuneCountInString(letter) > 4500 {
		return nil, fmt.Errorf("cover letter must be nonempty UTF-8, at most 4500 characters; got %d", utf8.RuneCountInString(letter))
	}
	if strings.TrimSpace(answers.FirstName) == "" || strings.TrimSpace(answers.LastName) == "" || strings.TrimSpace(answers.LocationID) == "" || answers.SalaryFrom <= 0 || (answers.SalaryCurrency != "RUB" && answers.SalaryCurrency != "USD") {
		return nil, fmt.Errorf("application answers require first_name, last_name, location_id, positive salary_from and RUB/USD salary_currency")
	}
	v, err := c.Vacancy(ctx, id)
	if err != nil {
		return nil, err
	}
	if v["is_active"] != true || v["offer_type"] != "vacancy" {
		return nil, fmt.Errorf("vacancy %s is inactive or uses an unsupported application route; stopped", id.String())
	}
	state, err := c.ApplicationState(ctx, id)
	if err != nil {
		return nil, err
	}
	if state["already_applied"] != false {
		return nil, fmt.Errorf("already applied to vacancy %s; no duplicate attempted", id.String())
	}
	if state["verified_profile"] != true {
		return nil, fmt.Errorf("native profile is not confirmed approved; wait for Getmatch review before applying")
	}
	profile, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	company, _ := v["company"].(map[string]any)
	name, _ := company["name"].(string)
	position, _ := v["position"].(string)
	if name == "" || position == "" {
		return nil, fmt.Errorf("vacancy %s destination is incomplete; stopped", id.String())
	}
	rolePath, _ := v["url"].(string)
	canonicalID, urlErr := domain.NewVacancyIDFromValue(Origin + rolePath)
	if urlErr != nil || canonicalID != id {
		return nil, fmt.Errorf("vacancy %s canonical URL is missing or differs; stopped", id.String())
	}
	// These fields change when viewing/applying; they are not role requirements.
	delete(v, "viewed")
	delete(v, "application")
	d := &ApplicationDraft{VacancyID: id.String(), VacancyURL: Origin + rolePath, Company: name, Position: position, VacancySHA256: digest(v), CandidateID: fmt.Sprint(state["candidate_id"]), ProfileSHA256: digest(profile), CoverLetter: letter, Answers: answers}
	d.ReviewSHA256 = draftDigest(*d)
	return d, nil
}

// SubmitApplication sends the actual requested application once. The review
// hash binds the destination, file bytes, letter, answers and shared profile.
func (c *Client) SubmitApplication(ctx context.Context, draft ApplicationDraft, reviewedSHA string) (Object, error) {
	if draft.CVPath != "" || draft.CVSHA256 != "" || draft.CVBytes != 0 {
		return nil, fmt.Errorf("PDF application sending is disabled: it overwrites the shared profile; historical packages are read-only and can still be verified")
	}
	if len(reviewedSHA) != 64 || draft.ReviewSHA256 != reviewedSHA || draftDigest(draft) != reviewedSHA {
		return nil, fmt.Errorf("review checksum differs from exact application package; no POST attempted")
	}
	id, err := domain.NewVacancyIDFromValue(draft.VacancyID)
	if err != nil {
		return nil, err
	}
	fresh, err := c.PrepareApplication(ctx, id, draft.CVPath, draft.CoverLetter, draft.Answers)
	if err != nil {
		return nil, err
	}
	if fresh.ReviewSHA256 != reviewedSHA {
		return nil, fmt.Errorf("role, shared profile or attachment changed since review; review fresh package before sending")
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fields := map[string]string{"first_name": draft.Answers.FirstName, "last_name": draft.Answers.LastName, "salary_from": strconv.Itoa(draft.Answers.SalaryFrom), "salary_currency": draft.Answers.SalaryCurrency, "location_id": draft.Answers.LocationID, "cover_letter": draft.CoverLetter, "web_apply_source": "vacancy", "web_page_source": "vacancy"}
	if len(draft.Answers.Citizenships) > 0 {
		b, _ := json.Marshal(draft.Answers.Citizenships)
		fields["citizenships"] = string(b)
	}
	if len(draft.Answers.WorkPermits) > 0 {
		b, _ := json.Marshal(draft.Answers.WorkPermits)
		fields["work_permits"] = string(b)
	}
	for key, value := range fields {
		if err = w.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	data, err := c.accountRequest(ctx, http.MethodPost, "/api/offers/"+id.String()+"/apply", w.FormDataContentType(), &body)
	if err != nil {
		return nil, fmt.Errorf("application attempt: %w; use application status/list before any retry", err)
	}
	var receipt Object
	if err = json.Unmarshal(data, &receipt); err != nil || receipt["application_id"] == nil {
		return nil, fmt.Errorf("application receipt missing; use application status/list before any retry")
	}
	result, verifyErr := c.VerifyApplication(ctx, draft, receipt)
	if verifyErr != nil {
		return Object{"submitted": true, "verified": false, "receipt": receipt, "vacancy_id": id.String(), "cv_sha256": draft.CVSHA256, "review_sha256": reviewedSHA, "verification_error": verifyErr.Error()}, nil
	}
	return result, nil
}

var letterURL = regexp.MustCompile(`https?://[^\s]+`)

// VerifyApplication is read-only and can resume verification after a completed
// or uncertain submission. URL masking is reported separately from exact text.
func (c *Client) VerifyApplication(ctx context.Context, draft ApplicationDraft, receipt Object) (Object, error) {
	if draftDigest(draft) != draft.ReviewSHA256 || receipt["application_id"] == nil {
		return nil, fmt.Errorf("review package or application receipt is invalid; no mutation attempted")
	}
	id, err := domain.NewVacancyIDFromValue(draft.VacancyID)
	if err != nil {
		return nil, err
	}
	result := Object{"verified": false, "receipt": receipt, "vacancy_id": id.String(), "cv_sha256": draft.CVSHA256, "review_sha256": draft.ReviewSHA256}
	state, err := c.ApplicationState(ctx, id)
	if err != nil {
		return nil, err
	}
	result["state"] = state
	if state["candidate_id"] != draft.CandidateID {
		result["verification_error"] = "candidate identity changed; do not resend"
		return result, nil
	}
	list, err := c.AllApplications(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range list["applications"].([]any) {
		row, _ := item.(map[string]any)
		if fmt.Sprint(row["id"]) != fmt.Sprint(receipt["application_id"]) {
			continue
		}
		result["application"] = row
		vacancy, _ := row["vacancy"].(map[string]any)
		company, _ := vacancy["company"].(map[string]any)
		status, _ := row["status"].(string)
		actualLetter, _ := row["cover_letter"].(string)
		exactLetter := actualLetter == draft.CoverLetter
		maskedLetter := !exactLetter && actualLetter == letterURL.ReplaceAllString(draft.CoverLetter, "•••••")
		result["cover_letter_exact"] = exactLetter
		result["cover_letter_matches_with_url_masking"] = maskedLetter
		if vacancy["url"] != strings.TrimPrefix(draft.VacancyURL, Origin) || vacancy["position"] != draft.Position || company["name"] != draft.Company || status == "" || state["already_applied"] != true || (!exactLetter && !maskedLetter) || row["salary_min"] != float64(draft.Answers.SalaryFrom) {
			result["verification_error"] = "listed application destination, letter, compensation or state differs; do not resend"
			return result, nil
		}
		result["verified"] = true
		if draft.CVPath == "" {
			return result, nil
		}
		_, hash, cvErr := c.ProfileCV(ctx)
		if cvErr != nil {
			result["cv_verification_error"] = cvErr.Error()
		} else {
			result["current_shared_cv_sha256"] = hash
			result["current_shared_cv_matches_reviewed"] = hash == draft.CVSHA256
		}
		return result, nil
	}
	result["verification_error"] = "returned application ID absent from complete applications list; inspect before any retry"
	return result, nil
}
