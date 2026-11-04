package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/meoyawn/clis/habr-career/internal/domain"
)

type ApplicationDraft struct {
	Vacancy           *Vacancy `json:"vacancy"`
	Resume            *Resume  `json:"resume"`
	SenderEmail       string   `json:"sender_email"`
	Letter            string   `json:"letter"`
	ResumeFingerprint string   `json:"resume_fingerprint"`
	ReviewSHA256      string   `json:"review_sha256"`
}

func digest(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Errorf("encode developer-owned review data: %w", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func draftDigest(draft ApplicationDraft) string {
	draft.ReviewSHA256 = ""
	return digest(draft)
}

func VerifyDraft(draft ApplicationDraft, approved string) error {
	if len(approved) != 64 || approved != draft.ReviewSHA256 || draftDigest(draft) != approved {
		return fmt.Errorf("review checksum is missing or differs from the unchanged application package; no request attempted")
	}
	if draft.Vacancy == nil || draft.Resume == nil {
		return fmt.Errorf("application draft has no vacancy or selected native resume; stopped")
	}
	return nil
}

func resumeContent(resume *Resume) *Resume {
	content := *resume
	content.User = Object{}
	for _, key := range []string{"id", "title", "site", "availability", "salary", "qualification", "specialization", "divisions", "skills", "age", "experience", "location", "contacts", "href", "relocation", "specializations", "citizenships", "foreignLanguages"} {
		if value, ok := resume.User[key]; ok {
			content.User[key] = value
		}
	}
	// Print text is the content boundary. Owner-only completion advice and company
	// ratings/subscriber counters are not applicant claims or review inputs.
	content.Fields = nil
	return &content
}

func (c *Client) PrepareApplication(ctx context.Context, vacancyID domain.VacancyID, resumeID domain.ResumeID, letter string) (*ApplicationDraft, error) {
	if !utf8.ValidString(letter) || strings.TrimSpace(letter) == "" {
		return nil, fmt.Errorf("letter must be nonempty valid UTF-8; no application attempted")
	}
	v, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return nil, err
	}
	if v.Archived || v.Hidden || v.Placeholder != nil || v.ExternalLink != nil && v.ExternalLink != "" || v.ResponseKind != "direct" {
		return nil, fmt.Errorf("vacancy %s is closed, restricted or not a supported native direct response; stopped", v.ID)
	}
	if v.UserAlias != resumeID.String() || v.Response != nil {
		return nil, fmt.Errorf("vacancy %s already has a response or selected resume %q is not signed-in alias %q; inspect application get before proceeding", v.ID, resumeID.String(), v.UserAlias)
	}
	left, ok := v.ResponsesLeft.(float64)
	if !ok || left < 1 || v.csrf == "" {
		return nil, fmt.Errorf("vacancy %s response allowance or CSRF protection is unavailable; stopped", v.ID)
	}
	if _, ok := v.Fields["message"]; !ok || len(v.Fields) < 1 || len(v.Fields) > 2 {
		return nil, fmt.Errorf("vacancy %s has an unsupported native response form; inspect its contract", v.ID)
	}
	for key := range v.Fields {
		if key != "message" && key != "salary" {
			return nil, fmt.Errorf("vacancy %s has unsupported form field %q; stopped", v.ID, key)
		}
	}
	r, err := c.Resume(ctx, resumeID)
	if err != nil {
		return nil, err
	}
	profile, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	if profile["alias"] != resumeID.String() {
		return nil, fmt.Errorf("account changed during application preparation; stopped")
	}
	email, _ := profile["email"].(string)
	if email == "" {
		return nil, fmt.Errorf("signed-in account has no confirmed sender email; stopped")
	}
	// Exclude changing counters and Habr's estimate, not employer or form evidence.
	v.PredictedSalary, v.ResponsesLeft = nil, nil
	r = resumeContent(r)
	draft := &ApplicationDraft{Vacancy: v, Resume: r, SenderEmail: email, Letter: letter, ResumeFingerprint: digest(r)}
	draft.ReviewSHA256 = draftDigest(*draft)
	return draft, nil
}

func (c *Client) SubmitApplication(ctx context.Context, draft ApplicationDraft, approved string) (Object, error) {
	if err := VerifyDraft(draft, approved); err != nil {
		return nil, err
	}
	vacancyID, err := domain.NewVacancyIDFromValue(draft.Vacancy.ID)
	if err != nil {
		return nil, err
	}
	resumeID, err := domain.NewResumeIDFromValue(draft.Resume.ID)
	if err != nil {
		return nil, err
	}
	fresh, err := c.PrepareApplication(ctx, vacancyID, resumeID, draft.Letter)
	if err != nil {
		return nil, err
	}
	if fresh.ReviewSHA256 != approved {
		return nil, fmt.Errorf("vacancy, native resume, sender or form changed since review; prepare/review the changed package before sending; no POST attempted")
	}
	path := "/api/frontend/vacancies/" + vacancyID.String() + "/responses"
	body, err := c.request(ctx, http.MethodPost, path, fresh.Vacancy.csrf, map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("application creation may be uncertain: %w; inspect application get; do not resubmit", err)
	}
	var receipt Object
	if json.Unmarshal(body, &receipt) != nil {
		return nil, fmt.Errorf("application creation returned no JSON receipt; inspect application get; do not resubmit")
	}
	response := object(receipt["response"])
	id := number(response["id"])
	if id == "" || object(response["author"])["id"] != resumeID.String() {
		return nil, fmt.Errorf("application creation receipt does not confirm a response ID and selected author; inspect application get; do not resubmit")
	}
	// Independently establish creation before the second native write.
	created, err := c.Application(ctx, vacancyID, resumeID)
	if err != nil || created["response_id"] != id {
		return nil, fmt.Errorf("creation returned response %s but independent readback failed (%v); inspect application get; do not resubmit", id, err)
	}
	if object(created["response"])["canUpdate"] != true {
		return nil, fmt.Errorf("response %s was created but native readback does not allow adding the letter; do not resubmit", id)
	}
	// Salary/currency are intentionally absent: they alter the shared profile.
	_, err = c.request(ctx, http.MethodPatch, path+"/"+id, fresh.Vacancy.csrf, map[string]string{"body": fresh.Letter})
	if err != nil {
		return nil, fmt.Errorf("response %s exists; saving its letter may be uncertain: %w; inspect application letter; do not resubmit", id, err)
	}
	return c.verifyApplication(ctx, *fresh, id)
}

// VerifyApplication recovers verification after an uncertain write without
// replaying either mutation. Habr strips terminal newlines from saved letters.
func (c *Client) VerifyApplication(ctx context.Context, draft ApplicationDraft) (Object, error) {
	return c.verifyApplication(ctx, draft, "")
}

func (c *Client) verifyApplication(ctx context.Context, draft ApplicationDraft, expectedResponseID string) (Object, error) {
	if err := VerifyDraft(draft, draft.ReviewSHA256); err != nil {
		return nil, err
	}
	vacancyID, err := domain.NewVacancyIDFromValue(draft.Vacancy.ID)
	if err != nil {
		return nil, err
	}
	resumeID, err := domain.NewResumeIDFromValue(draft.Resume.ID)
	if err != nil {
		return nil, err
	}
	verified, err := c.Application(ctx, vacancyID, resumeID)
	if err != nil {
		return nil, fmt.Errorf("application readback failed: %w; inspect application get; do not resubmit", err)
	}
	if verified["submitted"] != true || expectedResponseID != "" && verified["response_id"] != expectedResponseID {
		return nil, fmt.Errorf("application readback does not confirm the expected response identity %q; do not resubmit", expectedResponseID)
	}
	letter, ok := verified["letter"].(string)
	if !ok || strings.TrimRight(letter, "\r\n") != strings.TrimRight(draft.Letter, "\r\n") {
		return nil, fmt.Errorf("response %v exists but readback does not confirm the reviewed letter; inspect application letter; do not resubmit", verified["response_id"])
	}
	if letter != draft.Letter {
		verified["submitted_letter"] = draft.Letter
		verified["terminal_newlines_normalized"] = true
	}
	verified["verified"] = true
	verified["review_sha256"] = draft.ReviewSHA256
	return verified, nil
}
