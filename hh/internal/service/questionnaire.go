package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type ApplicationQuestion struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Answer string `json:"answer"`
}

// Only the reviewed questions are serialized. Fresh form nonces and CSRF fields
// stay in memory and are re-read immediately before the single approved POST.
type ApplicationQuestionnaire struct {
	TestID       string                `json:"test_id"`
	Name         string                `json:"name"`
	Instructions string                `json:"instructions"`
	Questions    []ApplicationQuestion `json:"questions"`
	status       Object
	fields       Object
}

func (c *Client) ApplicationQuestions(ctx context.Context, id VacancyID) (*ApplicationQuestionnaire, error) {
	body, err := c.request(ctx, http.MethodGet, "/applicant/vacancy_response", url.Values{"vacancyId": {id.String()}, "startedWithQuestion": {"false"}}, nil, true)
	if err != nil {
		return nil, err
	}
	state, err := DecodeState(body)
	if err != nil {
		return nil, err
	}
	statuses, _ := state["applicantVacancyResponseStatuses"].(map[string]any)
	status, _ := statuses[id.String()].(map[string]any)
	short, _ := status["shortVacancy"].(map[string]any)
	if nativeID(short["vacancyId"]) != id.String() {
		return nil, fmt.Errorf("questionnaire vacancy identity differs from %s; stopped", id)
	}
	visibility, _ := state["countriesProfileVisibilityAgreement"].(map[string]any)
	if visibility["show"] == true {
		return nil, fmt.Errorf("questionnaire requires country visibility agreement; stopped")
	}
	tests, _ := state["vacancyTests"].(map[string]any)
	test, _ := tests[id.String()].(map[string]any)
	testState, _ := status["test"].(map[string]any)
	if testState["hasTests"] != true || nativeID(testState["testId"]) == "" || test["required"] != "true" && test["required"] != "false" {
		return nil, fmt.Errorf("questionnaire has no confirmed native test state; stopped")
	}
	q := &ApplicationQuestionnaire{TestID: nativeID(testState["testId"]), status: status}
	q.Name, _ = test["name"].(string)
	instructions, _ := test["description"].(string)
	q.Instructions, err = PrintText([]byte(`<div id="HH-React-Root">` + instructions + `</div>`))
	if err != nil {
		return nil, err
	}
	tasks, _ := test["tasks"].([]any)
	seen := map[string]bool{}
	for _, item := range tasks {
		task, _ := item.(map[string]any)
		questionID := nativeID(task["id"])
		number, numberErr := strconv.ParseUint(questionID, 10, 64)
		choices, ok := task["candidateSolutions"].([]any)
		if numberErr != nil || number == 0 || seen[questionID] || task["open"] != "true" || !ok || len(choices) != 0 {
			return nil, fmt.Errorf("questionnaire supports only uniquely identified open-text questions; choices or unknown task format require CLI support, never skip the test")
		}
		promptHTML, _ := task["description"].(string)
		prompt, err := PrintText([]byte(`<div id="HH-React-Root">` + promptHTML + `</div>`))
		if err != nil || strings.TrimSpace(prompt) == "" {
			return nil, fmt.Errorf("question %s has no readable prompt; stopped", questionID)
		}
		seen[questionID] = true
		q.Questions = append(q.Questions, ApplicationQuestion{ID: questionID, Prompt: prompt})
	}
	if len(q.Questions) == 0 {
		return nil, fmt.Errorf("questionnaire has no supported questions; stopped")
	}
	q.fields, err = questionnaireFields(body)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"uidPk", "guid", "startTime"} {
		if q.fields[key] != test[key] {
			return nil, fmt.Errorf("questionnaire form and state disagree on %s; stopped", key)
		}
	}
	if q.fields["testRequired"] != test["required"] {
		return nil, fmt.Errorf("questionnaire requirement differs between form and state; stopped")
	}
	return q, nil
}

func questionnaireFields(body []byte) (Object, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	fields := Object{}
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, inForm bool) {
		if n.Type == html.ElementNode && n.Data == "form" {
			inForm = attr(n, "name") == "vacancy_response" && strings.EqualFold(attr(n, "method"), "post") && attr(n, "action") == ""
		}
		if inForm && n.Type == html.ElementNode && n.Data == "input" && attr(n, "type") == "hidden" {
			fields[attr(n, "name")] = attr(n, "value")
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, inForm)
		}
	}
	walk(doc, false)
	if len(fields) != 5 {
		return nil, fmt.Errorf("questionnaire hidden fields changed or require an access challenge; stopped")
	}
	for _, key := range []string{"_xsrf", "uidPk", "guid", "startTime", "testRequired"} {
		if value, ok := fields[key].(string); !ok || value == "" {
			return nil, fmt.Errorf("questionnaire form lacks %s; stopped", key)
		}
	}
	return fields, nil
}
