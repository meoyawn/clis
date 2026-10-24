package service

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// This synthetic capture follows the independently recorded native SDUI contract.
// No real account data, cookies or recorded media identifiers belong in fixtures.
func captureFixture(t *testing.T, notify bool) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/experience-save.har.json")
	if err != nil {
		t.Fatal(err)
	}
	if !notify {
		return data
	}
	var har map[string]any
	if err := json.Unmarshal(data, &har); err != nil {
		t.Fatal(err)
	}
	entry := har["log"].(map[string]any)["entries"].([]any)[0].(map[string]any)
	post := entry["request"].(map[string]any)["postData"].(map[string]any)
	var body map[string]any
	if err := json.Unmarshal([]byte(post["text"].(string)), &body); err != nil {
		t.Fatal(err)
	}
	for _, group := range []any{body["states"], body["requestedArguments"].(map[string]any)["states"]} {
		for _, row := range group.([]any) {
			state := row.(map[string]any)
			if state["key"] == "notify" {
				state["value"] = true
			}
		}
	}
	changed, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	post["text"] = string(changed)
	data, err = json.Marshal(har)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestExperienceDescriptionPreservesNativeFormAndRefreshesBaseline(t *testing.T) {
	capture, err := LoadExperienceCapture(bytes.NewReader(captureFixture(t, false)), "42")
	if err != nil {
		t.Fatal(err)
	}
	data, err := capture.PrepareDescription("Website CV text")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "never-copy-this") {
		t.Fatal("HAR credentials entered the native body")
	}
	groups := []any{body["states"], body["requestedArguments"].(map[string]any)["states"]}
	for _, group := range groups {
		states := group.([]any)
		if states[0].(map[string]any)["value"] != "Website CV text" ||
			states[1].(map[string]any)["value"] != "Current text" ||
			states[2].(map[string]any)["value"] != false ||
			states[3].(map[string]any)["value"] != "Founder" {
			t.Fatal("description update changed unrelated state or kept an obsolete comparison baseline")
		}
	}
	payload := body["requestedArguments"].(map[string]any)["payload"].(map[string]any)
	media := payload["mediaItems"].([]any)[0].(map[string]any)
	if payload["profileId"] != "ACoExamplePerson" || payload["positionId"] != "42" || media["mediaId"] != "77" || media["title"] != "Portfolio" {
		t.Fatal("description update changed owner, position or attached media")
	}
}

func TestExperienceRejectsUnsafeCaptureAndNativeErrorReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, position string
		notify         bool
	}{
		{"other position", "43", false}, {"broadcast enabled", "42", true}, {"invalid position", "../42", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadExperienceCapture(bytes.NewReader(captureFixture(t, tc.notify)), tc.position); err == nil {
				t.Fatal("unsafe capture accepted")
			}
		})
	}
	for _, receipt := range []string{
		`0:{"response":{"errors":[{"message":"validation failed"}],"completionAction":{"actions":[{}]}}}`,
		`0:{"response":{"completionAction":{"actions":[{}]}}}`,
		`0:{"response":{"errors":[]}}`,
		`<html>Sign in</html>`,
	} {
		if err := ValidateExperienceResponse([]byte(receipt)); err == nil {
			t.Fatalf("unconfirmed save accepted: %s", receipt)
		}
	}
}
