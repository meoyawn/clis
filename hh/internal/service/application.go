package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode/utf8"
)

// ApplicationDraft is the complete reviewable package. Its checksum binds the
// exact letter, destination, selected resume and current resume content.
type ApplicationDraft struct {
	Vacancy           *Vacancy                  `json:"vacancy"`
	ResumeID          string                    `json:"resume_id"`
	ResumeURL         string                    `json:"resume_url"`
	ResumeTitle       any                       `json:"resume_title"`
	ResumeSalary      any                       `json:"resume_salary"`
	ResumeEmail       []string                  `json:"resume_email"`
	ResumeFingerprint string                    `json:"resume_fingerprint"`
	Letter            string                    `json:"letter"`
	Request           Object                    `json:"request"`
	ReviewSHA256      string                    `json:"review_sha256"`
	Questionnaire     *ApplicationQuestionnaire `json:"questionnaire,omitempty"`
}

func applicationDigest(draft ApplicationDraft) string {
	draft.ReviewSHA256 = ""
	data, _ := json.Marshal(draft)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hasNativeID(value any, id string) bool {
	items, _ := value.([]any)
	for _, item := range items {
		if nativeID(item) == id {
			return true
		}
	}
	return false
}

func (c *Client) PrepareApplication(ctx context.Context, vacancyID VacancyID, resumeID ResumeID, letter string, answers map[string]string) (*ApplicationDraft, error) {
	if !utf8.ValidString(letter) {
		return nil, fmt.Errorf("letter is not valid UTF-8; no application attempted")
	}
	vacancy, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return nil, err
	}
	if vacancy.Archived || vacancy.Closed {
		return nil, fmt.Errorf("vacancy %s is archived or closed; stopped", vacancyID)
	}
	options, err := c.ResponseOptions(ctx, vacancyID)
	if err != nil {
		return nil, err
	}
	var questionnaire *ApplicationQuestionnaire
	if options["type"] == "test-required" {
		questionnaire, err = c.ApplicationQuestions(ctx, vacancyID)
		if err != nil {
			return nil, err
		}
	} else if options["type"] != "modal" {
		return nil, fmt.Errorf("unsupported response type %v for vacancy %s; no quick response or submission attempted", options["type"], vacancyID)
	}
	popup, _ := options["responsePopup"].(map[string]any)
	popupVacancy, _ := popup["vacancy"].(map[string]any)
	if questionnaire == nil && (popup["type"] != "normal" || nativeID(popupVacancy["vacancyId"]) != vacancyID.String()) {
		return nil, fmt.Errorf("response form type or vacancy identity is unsupported; no application attempted")
	}
	for _, key := range []string{"countriesProfileVisibilityAgreement", "relocationWarning"} {
		warning, _ := options[key].(map[string]any)
		if warning["show"] == true {
			return nil, fmt.Errorf("response requires %s; resolve its terms with the human before applying", key)
		}
	}
	status, _ := options["responseStatus"].(map[string]any)
	if questionnaire != nil {
		status = questionnaire.status
		if len(answers) != len(questionnaire.Questions) {
			return nil, fmt.Errorf("provide exactly %d reviewed questionnaire answers with --answers-file; no application attempted", len(questionnaire.Questions))
		}
		for i, question := range questionnaire.Questions {
			answer, exists := answers[question.ID]
			if !exists || !utf8.ValidString(answer) || strings.TrimSpace(answer) == "" || utf8.RuneCountInString(answer) > 10000 {
				return nil, fmt.Errorf("question %s needs a nonempty UTF-8 answer of at most 10000 characters; stopped", question.ID)
			}
			questionnaire.Questions[i].Answer = answer
		}
	} else if len(answers) != 0 {
		return nil, fmt.Errorf("answers supplied for a response without a supported questionnaire; stopped")
	}
	if status["alreadyApplied"] != false || status["responseImpossible"] != false {
		return nil, fmt.Errorf("response is already applied, restricted, or unknown for vacancy %s; stopped", vacancyID)
	}
	allowed, _ := status["by_country_applicant_visibility"].(map[string]any)
	if allowed["responseAllowed"] != true {
		return nil, fmt.Errorf("country visibility does not confirm response eligibility; stopped")
	}
	test, _ := status["test"].(map[string]any)
	if questionnaire == nil && test["hasTests"] != false {
		return nil, fmt.Errorf("vacancy requires tests or has unknown test state; no application attempted")
	}
	short, _ := status["shortVacancy"].(map[string]any)
	company, _ := short["company"].(map[string]any)
	if nativeID(short["vacancyId"]) != vacancyID.String() || nativeID(company["id"]) != nativeID(vacancy.Employer["id"]) {
		return nil, fmt.Errorf("response destination differs from requested vacancy/employer; stopped")
	}
	max, ok := status["letterMaxLength"].(float64)
	if !ok || max < 1 || utf8.RuneCountInString(letter) > int(max) {
		return nil, fmt.Errorf("letter length %d exceeds confirmed limit %v (or limit is unknown)", utf8.RuneCountInString(letter), status["letterMaxLength"])
	}
	if short["@responseLetterRequired"] == true && strings.TrimSpace(letter) == "" {
		return nil, fmt.Errorf("vacancy requires a nonempty cover letter")
	}
	resumes, _ := status["resumes"].(map[string]any)
	var selected Object
	var numericID string
	for key, value := range resumes {
		row, _ := value.(map[string]any)
		if row["hash"] == resumeID.String() {
			if selected != nil {
				return nil, fmt.Errorf("duplicate selected resume identity in response options; stopped")
			}
			selected, numericID = row, key
		}
	}
	if selected == nil || selected["isIncomplete"] != false || !hasNativeID(status["unusedResumeIds"], numericID) || hasNativeID(status["usedResumeIds"], numericID) {
		return nil, fmt.Errorf("selected resume %s is not confirmed eligible and unused for this vacancy; stopped", resumeID)
	}
	inconsistencies, _ := status["resumeInconsistencies"].(map[string]any)
	if reasons, exists := inconsistencies[numericID]; exists {
		return nil, fmt.Errorf("selected resume has response inconsistencies: %v; review before applying", reasons)
	}
	resume, err := c.Get(ctx, resumeID)
	if err != nil {
		return nil, err
	}
	attrs, _ := resume.Fields["_attributes"].(map[string]any)
	if nativeID(attrs["id"]) != numericID {
		return nil, fmt.Errorf("selected resume numeric identity differs between form and export; stopped")
	}
	view, err := c.View(ctx, resumeID)
	if err != nil {
		return nil, err
	}
	fingerprint, _ := json.Marshal(Object{"fields": isolatedState(resume.Fields), "print": view.Text})
	resumeSum := sha256.Sum256(fingerprint)
	fields := Editable(resume.Fields)
	vacancy.ResponseState = nil // do not copy unrelated resumes into a review package.
	draft := &ApplicationDraft{Vacancy: vacancy, ResumeID: resumeID.String(), ResumeURL: view.URL, ResumeTitle: fields["title"], ResumeSalary: fields["salary"], ResumeEmail: view.Contacts.Email, ResumeFingerprint: hex.EncodeToString(resumeSum[:]), Letter: letter,
		Request: Object{"vacancy_id": vacancyID.String(), "resume_hash": resumeID.String(), "letter": letter, "lux": "true", "ignore_postponed": "true", "incomplete": "false", "withoutTest": "no", "mark_applicant_visible_in_vacancy_country": "false", "country_ids": "[]"}}
	draft.Questionnaire = questionnaire
	if questionnaire != nil {
		draft.Request["hhtmFromLabel"] = ""
		draft.Request["hhtmSourceLabel"] = ""
		for _, question := range questionnaire.Questions {
			draft.Request["task_"+question.ID+"_text"] = question.Answer
		}
	}
	draft.ReviewSHA256 = applicationDigest(*draft)
	return draft, nil
}

func (c *Client) SubmitApplication(ctx context.Context, draft ApplicationDraft, approvedSHA string) (Object, error) {
	if len(approvedSHA) != 64 || approvedSHA != draft.ReviewSHA256 || applicationDigest(draft) != approvedSHA {
		return nil, fmt.Errorf("approval checksum is missing or differs from this exact application package; no request attempted")
	}
	if draft.Vacancy == nil {
		return nil, fmt.Errorf("approved application has no vacancy; stopped")
	}
	vacancyID, err := NewVacancyIDFromValue(draft.Vacancy.ID)
	if err != nil {
		return nil, err
	}
	resumeID, err := NewResumeIDFromValue(draft.ResumeID)
	if err != nil {
		return nil, err
	}
	answers := map[string]string{}
	if draft.Questionnaire != nil {
		for _, question := range draft.Questionnaire.Questions {
			answers[question.ID] = question.Answer
		}
	}
	fresh, err := c.PrepareApplication(ctx, vacancyID, resumeID, draft.Letter, answers)
	if err != nil {
		return nil, err
	}
	if fresh.ReviewSHA256 != approvedSHA {
		return nil, fmt.Errorf("vacancy, resume, or application package changed since approval; show the fresh package and obtain renewed approval; no POST attempted")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	request := Object{}
	for key, value := range fresh.Request {
		request[key] = value
	}
	if fresh.Questionnaire != nil {
		for key, value := range fresh.Questionnaire.fields {
			request[key] = value
		}
	}
	for key, value := range request {
		if err := writer.WriteField(key, value.(string)); err != nil {
			return nil, fmt.Errorf("encode application field %s: %w", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	data, err := c.requestBody(ctx, http.MethodPost, "/applicant/vacancy_response/popup", nil, request, false, &body, writer.FormDataContentType())
	if err != nil {
		return nil, fmt.Errorf("application outcome may be uncertain: %w; inspect response-options before retrying", err)
	}
	var receipt Object
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("decode application receipt: %w; inspect before retrying", err)
	}
	topic := nativeID(receipt["topic_id"])
	if topic == "" || topic == "SPAM" || topic == "0" {
		return nil, fmt.Errorf("application has no confirmed topic receipt: %s; inspect before retrying", boundedResponse(data))
	}
	readback, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return nil, fmt.Errorf("application returned topic %s but verification failed: %w; do not resend", topic, err)
	}
	status := readback.ResponseState
	negotiations, _ := status["negotiations"].(map[string]any)
	topics, _ := negotiations["topicList"].([]any)
	for _, value := range topics {
		row, _ := value.(map[string]any)
		if nativeID(row["id"]) == topic {
			resumes, _ := status["resumes"].(map[string]any)
			resume, _ := resumes[nativeID(row["resumeId"])].(map[string]any)
			if resume["hash"] == resumeID.String() && hasNativeID(status["usedResumeIds"], nativeID(row["resumeId"])) {
				return Object{"submitted": true, "verified": true, "vacancy_id": vacancyID.String(), "resume_id": resumeID.String(), "topic_id": topic, "review_sha256": approvedSHA}, nil
			}
		}
	}
	return nil, fmt.Errorf("topic %s was returned but independent response readback does not confirm the selected resume; do not resend", topic)
}
