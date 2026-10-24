package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ApplicationStatus is a live negotiation, not a locally prepared application.
// Letter presence and a recorded send receipt are deliberately separate evidence.
type ApplicationStatus struct {
	VacancyID      string          `json:"vacancy_id"`
	VacancyURL     string          `json:"vacancy_url"`
	Title          string          `json:"title"`
	Employer       Object          `json:"employer"`
	TopicID        string          `json:"topic_id"`
	ChatID         string          `json:"chat_id"`
	ApplicantID    string          `json:"applicant_user_id"`
	ResumeID       string          `json:"resume_id"`
	ResumeURL      string          `json:"resume_url"`
	CreatedAt      string          `json:"created_at"`
	State          string          `json:"state"`
	InitialType    string          `json:"initial_topic_type"`
	Submitted      bool            `json:"submitted_by_applicant"`
	Archived       bool            `json:"archived"`
	Viewed         bool            `json:"viewed_by_employer"`
	Verified       bool            `json:"verified"`
	Scope          string          `json:"verification_scope"`
	CoverLetter    Object          `json:"cover_letter"`
	RecordedLetter *RecordedLetter `json:"recorded_letter,omitempty"`
}

type RecordedLetter struct {
	HAR          string `json:"har"`
	HARSHA256    string `json:"har_sha256"`
	RecordedAt   string `json:"recorded_at"`
	TopicID      string `json:"topic_id"`
	Text         string `json:"text"`
	Status       int    `json:"http_status"`
	Accepted     bool   `json:"server_accepted"`
	LivePresence bool   `json:"live_presence_verified"`
	TextReadback bool   `json:"current_text_readback_verified"`
	Scope        string `json:"verification_scope"`
}

func negotiationTopics(v *Vacancy) ([]any, error) {
	n, ok := v.ResponseState["negotiations"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("vacancy %s has no negotiation state; cannot confirm an application", v.ID)
	}
	rows, ok := n["topicList"].([]any)
	if !ok {
		return nil, fmt.Errorf("vacancy %s has no negotiation topic list", v.ID)
	}
	return rows, nil
}

func (c *Client) applicationStatus(v *Vacancy, row Object) (*ApplicationStatus, error) {
	topic, vacancy, resume := nativeID(row["id"]), nativeID(row["vacancyId"]), nativeID(row["resumeId"])
	if !vacancyNumber.MatchString(topic) || vacancy != v.ID || nativeID(row["employerId"]) != nativeID(v.Employer["id"]) {
		return nil, fmt.Errorf("negotiation %s does not confirm vacancy %s and its employer", topic, v.ID)
	}
	resumes, _ := v.ResponseState["resumes"].(map[string]any)
	selected, _ := resumes[resume].(map[string]any)
	hash, _ := selected["hash"].(string)
	id, err := NewResumeIDFromValue(hash)
	if err != nil {
		return nil, fmt.Errorf("negotiation %s resume %s has no valid public hash: %w", topic, resume, err)
	}
	letter, letterOK := row["hasResponseLetter"].(bool)
	viewed, viewedOK := row["viewedByOpponent"].(bool)
	archived, archivedOK := row["archived"].(bool)
	state, stateOK := row["lastState"].(string)
	if !letterOK || !viewedOK || !archivedOK || !stateOK {
		return nil, fmt.Errorf("negotiation %s has unknown letter, view, archive or state flags", topic)
	}
	resources, _ := row["resources"].([]any)
	resourceLetter := false
	for _, item := range resources {
		r, _ := item.(map[string]any)
		resourceLetter = resourceLetter || r["type"] == "RESPONSE_LETTER"
	}
	if resourceLetter != letter {
		return nil, fmt.Errorf("negotiation %s has conflicting response-letter evidence", topic)
	}
	created, _ := row["creationTime"].(string)
	initialType, _ := row["initialTopicType"].(string)
	viewURL, _ := c.URLs(id)
	return &ApplicationStatus{VacancyID: v.ID, VacancyURL: v.URL, Title: v.Title, Employer: v.Employer,
		TopicID: topic, ChatID: nativeID(row["chatId"]), ApplicantID: nativeID(row["applicantUserId"]), ResumeID: hash, ResumeURL: viewURL,
		CreatedAt: created, State: state, InitialType: initialType, Submitted: initialType == "RESPONSE_BY_APPLICANT",
		Archived: archived, Viewed: viewed, Verified: true,
		Scope: "live vacancy, employer, topic and selected resume identity; current letter text is not read back",
		CoverLetter: Object{"present": letter, "text_available": false,
			"text_source":        "not exposed by the recorded vacancy/negotiation read endpoints",
			"ui_add_available":   state == "RESPONSE" && !viewed && !archived && !letter,
			"server_editability": "unknown; no edit attempted"}}, nil
}

// Application reads the exact selected resume's negotiation. It never infers
// submission from alreadyApplied, which can be false even with a live topic.
func (c *Client) Application(ctx context.Context, vacancyID VacancyID, resumeID ResumeID) (*ApplicationStatus, error) {
	v, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return nil, err
	}
	rows, err := negotiationTopics(v)
	if err != nil {
		return nil, err
	}
	var result *ApplicationStatus
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("vacancy %s has a malformed negotiation", vacancyID)
		}
		status, err := c.applicationStatus(v, row)
		if err != nil {
			return nil, err
		}
		if status.ResumeID != resumeID.String() {
			continue
		}
		if result != nil {
			return nil, fmt.Errorf("vacancy %s has multiple negotiations for resume %s; no default topic selected", vacancyID, resumeID)
		}
		result = status
	}
	if result == nil {
		return nil, fmt.Errorf("no existing application to vacancy %s with resume %s", vacancyID, resumeID)
	}
	return result, nil
}

// Applications follows HH's active list and its zero-based page parameter.
// Every returned topic is independently matched against its vacancy/resume.
func (c *Client) Applications(ctx context.Context) (Object, error) {
	rows := []Object{}
	seen := map[string]bool{}
	pageCount, total := 1, -1
	for page := 0; page < pageCount; page++ {
		query := url.Values{}
		if page > 0 {
			query.Set("page", strconv.Itoa(page))
		}
		body, err := c.request(ctx, http.MethodGet, "/applicant/negotiations", query, nil, true)
		if err != nil {
			return nil, err
		}
		state, err := DecodeState(body)
		if err != nil {
			return nil, err
		}
		list, _ := state["applicantNegotiations"].(map[string]any)
		if list["filterInUse"] != "active" {
			return nil, fmt.Errorf("negotiation list filter %v is not active; refusing a misleading list", list["filterInUse"])
		}
		pages, err := strconv.Atoi(nativeID(list["pageCount"]))
		if err != nil || pages < 0 || pages > 1000 {
			return nil, fmt.Errorf("invalid negotiation page count %v", list["pageCount"])
		}
		if pages == 0 {
			pages = 1 // an empty result still has one response to validate
		}
		count, err := strconv.Atoi(nativeID(list["total"]))
		if err != nil || count < 0 {
			return nil, fmt.Errorf("invalid negotiation total %v", list["total"])
		}
		if page > 0 && (pages != pageCount || count != total) {
			return nil, fmt.Errorf("active negotiation list changed during pagination; no complete result emitted")
		}
		pageCount, total = pages, count
		items, ok := list["topicList"].([]any)
		if !ok {
			return nil, fmt.Errorf("active negotiation page %d has no topic list", page)
		}
		for _, item := range items {
			row, ok := item.(map[string]any)
			id := nativeID(row["id"])
			if !ok || !vacancyNumber.MatchString(id) || seen[id] || row["archived"] != false || row["applicantSubState"] != "SHOW" {
				return nil, fmt.Errorf("active negotiation page %d has a duplicate, hidden, archived or invalid topic %s", page, id)
			}
			seen[id] = true
			rows = append(rows, row)
		}
	}
	if len(rows) != total {
		return nil, fmt.Errorf("active negotiation count mismatch: read %d, expected %d; no partial list emitted", len(rows), total)
	}
	applications := []*ApplicationStatus{}
	vacancies := map[string]*Vacancy{}
	for _, row := range rows {
		id, err := NewVacancyIDFromValue(nativeID(row["vacancyId"]))
		if err != nil {
			return nil, err
		}
		v := vacancies[id.String()]
		if v == nil {
			v, err = c.Vacancy(ctx, id)
			if err != nil {
				return nil, err
			}
			vacancies[id.String()] = v
		}
		topics, err := negotiationTopics(v)
		if err != nil {
			return nil, err
		}
		var matched Object
		for _, item := range topics {
			topic, _ := item.(map[string]any)
			if nativeID(topic["id"]) == nativeID(row["id"]) && nativeID(topic["resumeId"]) == nativeID(row["resumeId"]) {
				if matched != nil {
					return nil, fmt.Errorf("duplicate topic %s in vacancy readback", nativeID(row["id"]))
				}
				matched = topic
			}
		}
		if matched == nil || matched["archived"] != false || matched["applicantSubState"] != "SHOW" {
			return nil, fmt.Errorf("active topic %s is not confirmed by vacancy readback", nativeID(row["id"]))
		}
		status, err := c.applicationStatus(v, matched)
		if err != nil {
			return nil, err
		}
		applications = append(applications, status)
	}
	return Object{"filter": "active", "total": total, "pages": pageCount, "complete": true,
		"source_url": c.base.String() + "/applicant/negotiations", "applications": applications}, nil
}

// VerifyRecordedLetter reads a local HAR; it never replays its requests or imports
// credentials. A matching recorded success is not a current-text readback.
func VerifyRecordedLetter(status *ApplicationStatus, path string) error {
	if status == nil || !status.Verified || !vacancyNumber.MatchString(status.TopicID) {
		return fmt.Errorf("letter HAR requires a verified live application status with a valid topic ID")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read letter HAR %q: %w", path, err)
	}
	var har struct {
		Log struct {
			Entries []struct {
				Started string `json:"startedDateTime"`
				Request struct {
					Method, URL string
					PostData    struct {
						Params []struct{ Name, Value string }
					}
				}
				Response struct {
					Status  int
					Content struct{ Text, Encoding string }
				}
			}
		}
	}
	if err := json.Unmarshal(data, &har); err != nil {
		return fmt.Errorf("decode letter HAR %q: %w", path, err)
	}
	base, err := url.Parse(status.VacancyURL)
	if err != nil || base.Host == "" {
		return fmt.Errorf("invalid application vacancy URL; cannot bind a letter HAR")
	}
	var evidence *RecordedLetter
	for _, entry := range har.Log.Entries {
		u, err := url.Parse(entry.Request.URL)
		if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Path != "/applicant/vacancy_response/edit_ajax" || entry.Request.Method != http.MethodPost {
			continue
		}
		fields := map[string]string{}
		for _, param := range entry.Request.PostData.Params {
			if param.Name != "text" && param.Name != "topicId" {
				continue
			}
			if _, exists := fields[param.Name]; exists {
				return fmt.Errorf("HAR letter request repeats field %s; cannot bind its receipt", param.Name)
			}
			fields[param.Name] = param.Value
		}
		if fields["topicId"] != status.TopicID {
			continue
		}
		content := []byte(entry.Response.Content.Text)
		if entry.Response.Content.Encoding == "base64" {
			content, err = base64.StdEncoding.DecodeString(entry.Response.Content.Text)
			if err != nil {
				return fmt.Errorf("decode HAR letter receipt: %w", err)
			}
		} else if entry.Response.Content.Encoding != "" {
			return fmt.Errorf("unsupported HAR receipt encoding %q", entry.Response.Content.Encoding)
		}
		var receipt Object
		if json.Unmarshal(content, &receipt) != nil || entry.Response.Status != 200 || receipt["success"] != "true" || receipt["error"] != nil || fields["text"] == "" {
			return fmt.Errorf("HAR has no successful nonempty letter receipt for topic %s", status.TopicID)
		}
		if evidence != nil {
			return fmt.Errorf("HAR has multiple letter writes for topic %s; cannot claim a single final text", status.TopicID)
		}
		sum := sha256.Sum256(data)
		evidence = &RecordedLetter{HAR: path, HARSHA256: hex.EncodeToString(sum[:]), RecordedAt: entry.Started,
			TopicID: status.TopicID, Text: fields["text"], Status: entry.Response.Status, Accepted: true,
			Scope: "matching topic in recorded POST and success receipt; live letter presence only; current text is not exposed"}
	}
	if evidence == nil {
		return fmt.Errorf("HAR has no matching letter POST for live topic %s", status.TopicID)
	}
	if status.CoverLetter["present"] != true {
		return fmt.Errorf("HAR recorded success but live topic %s does not confirm letter presence", status.TopicID)
	}
	evidence.LivePresence = true
	if status.CoverLetter["current_text_readback_verified"] == true {
		current, ok := status.CoverLetter["text"].(string)
		if !ok || strings.ReplaceAll(evidence.Text, "\r\n", "\n") != current {
			return fmt.Errorf("HAR letter text differs from the current verified chat text; read without --har to inspect the live letter")
		}
		evidence.TextReadback = true
		evidence.Scope = "recorded topic/POST/success plus current linked chat-letter text; CRLF is normalized to LF for comparison"
	}
	status.RecordedLetter = evidence
	return nil
}
