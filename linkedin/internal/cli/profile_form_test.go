package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func aboutFormFixture(text string, skills []string) string {
	payload := map[string]any{"profileId": "ACoExamplePerson", "vanityName": "example-person"}
	var states, keys, items []any
	for _, skill := range skills {
		items = append(items, map[string]any{"key": skill})
	}
	for _, field := range []string{"about", "initialAbout", "skills", "initialSkills"} {
		key := map[string]any{"key": map[string]any{"value": map[string]any{"$case": "id", "id": field}}}
		keys = append(keys, key)
		value := map[string]any{"$case": "stringValue", "stringValue": text}
		if field == "skills" {
			value = map[string]any{"$case": "expression", "expression": map[string]any{"expression": map[string]any{"$case": "stringListExpression", "stringListExpression": map[string]any{"expression": map[string]any{"$case": "collectionIdsStringList", "collectionIdsStringList": map[string]any{"collectionId": "top-skills"}}}}}}
		}
		if field == "initialSkills" {
			value = map[string]any{"$case": "stringListValue", "stringListValue": map[string]any{"stringValues": skills}}
		}
		states = append(states, map[string]any{"key": map[string]any{"key": key["key"]}, "value": value})
		payload[field] = map[string]any{"key": field, "namespace": "MemoryNamespace"}
	}
	row := map[string]any{"modelStates": states, "collection": map[string]any{"collectionId": "top-skills", "initialItems": items}, "save": map[string]any{
		"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{"requestId": "com.linkedin.sdui.requests.profile.saveProfileAboutForm", "requestedArguments": map[string]any{"requestedStateKeys": keys, "payload": payload}},
	}}
	data, _ := json.Marshal(row)
	chunks, _ := json.Marshal([]string{"0:" + string(data) + "\n"})
	return `<html><script>window.__como_rehydration__ = ` + string(chunks) + `;</script></html>`
}

func TestAboutNativeSavePreservesSkillsAndLocationAndRequiresReadback(t *testing.T) {
	const replacement = "Build complete products.\nAutomate publishing and marketing."
	for _, scenario := range []string{"preview", "apply", "wrong-owner", "stale-form", "timeout", "error-receipt", "ignored-save", "changed-location", "changed-skills"} {
		t.Run(scenario, func(t *testing.T) {
			current, location := "Existing About", "Existing location"
			skills := []string{"ProfileSkill:1", "ProfileSkill:2"}
			posts := 0
			client := linkedinClient{session: session{LIAT: "synthetic", JSessionID: "ajax:synthetic"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				client.lastRequestAt = time.Time{}
				switch r.URL.Path {
				case "/voyager/api/me":
					owner := "ACoExamplePerson"
					if scenario == "wrong-owner" {
						owner = "ACoSomeoneElse"
					}
					return jsonResponse(fmt.Appendf(nil, `{"data":{"*miniProfile":"urn:li:fs_miniProfile:%s"}}`, owner)), nil
				case "/voyager/api/identity/dash/profiles":
					var data map[string]any
					if err := json.Unmarshal(fixture(t, "profile"), &data); err != nil {
						t.Fatal(err)
					}
					data["included"].([]any)[1].(map[string]any)["summary"] = current
					data["included"].([]any)[1].(map[string]any)["headline"] = "Software engineer"
					encoded, _ := json.Marshal(data)
					return jsonResponse(encoded), nil
				case "/in/example-person/edit/forms/summary/new/":
					text := current
					if scenario == "stale-form" {
						text = "Concurrent edit"
					}
					return jsonResponse([]byte(aboutFormFixture(text, skills))), nil
				case "/in/example-person/edit/intro/":
					return jsonResponse([]byte(headlineFormFixture("Software engineer", location))), nil
				case "/flagship-web/rsc-action/actions/server-request":
					posts++
					if r.Method != http.MethodPost || r.URL.Query().Get("sduiid") != "com.linkedin.sdui.requests.profile.saveProfileAboutForm" {
						t.Fatalf("unexpected save: %s %s", r.Method, r.URL)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					values := map[string]any{}
					for _, item := range body["states"].([]any) {
						state := item.(map[string]any)
						values[state["key"].(string)] = state["value"]
					}
					want := map[string]any{"about": replacement, "initialAbout": "Existing About", "skills": []any{"ProfileSkill:1", "ProfileSkill:2"}, "initialSkills": []any{"ProfileSkill:1", "ProfileSkill:2"}}
					if !reflect.DeepEqual(values, want) {
						t.Fatalf("native save changed the skill list or baseline: %v", values)
					}
					if scenario == "timeout" {
						return nil, context.DeadlineExceeded
					}
					if scenario == "error-receipt" {
						return jsonResponse([]byte(`0:{"response":{"errors":[{"message":"Rejected"}],"completionAction":{"actions":[{}]}}}`)), nil
					}
					if scenario != "ignored-save" {
						current = replacement
					}
					if scenario == "changed-location" {
						location = "Changed location"
					}
					if scenario == "changed-skills" {
						skills = []string{"ProfileSkill:1"}
					}
					return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[{"saved":true}]}}}`)), nil
				default:
					return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
			})}
			ref, _ := parseProfile("example-person")
			spec := profileFormSpec{path: "edit/forms/summary/new/", screen: "com.linkedin.sdui.flagshipnav.profile.ProfileAboutForm", initialRequest: saveAbout, saveRequest: saveAbout, fieldRequest: saveAbout}
			result, err := client.editProfileForm(context.Background(), ref, spec, map[string]any{"about": replacement}, scenario != "preview", t.TempDir())
			wantError := map[string]string{"wrong-owner": "signed-in owner", "stale-form": "differs from current profile", "timeout": "outcome uncertain", "error-receipt": "error-free completion", "ignored-save": "did not match", "changed-location": "intro fields changed", "changed-skills": "unrequested form fields changed"}[scenario]
			wantPosts := 1
			if scenario == "preview" || scenario == "wrong-owner" || scenario == "stale-form" {
				wantPosts = 0
			}
			if posts != wantPosts {
				t.Fatalf("got %d saves, expected %d; retries are forbidden", posts, wantPosts)
			}
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("expected %q, got %v", wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preview" && (result.Applied || result.Verified) {
				t.Fatal("preview claimed a saved profile")
			}
			if scenario == "apply" {
				if !result.Applied || !result.Verified {
					t.Fatalf("save not verified: %+v", result)
				}
				info, err := os.Stat(result.RecordPath)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("private backup missing: %v", err)
				}
			}
		})
	}
}

func TestNativePositionCompanyRequirementUsesTheCurrentEmploymentType(t *testing.T) {
	const nativeExpression = `{"expression":{"$case":"booleanExpression","booleanExpression":{"expression":{"$case":"notExpression","notExpression":{"a":{"expression":{"$case":"orExpression","orExpression":{"a":{"expression":{"$case":"stringEqualsExpression","stringEqualsExpression":{"ignoreCase":false,"a":{"expression":{"$case":"bindableString","bindableString":{"key":{"key":{"value":{"id":"employment"}}}}}},"b":{"expression":{"$case":"stringValue","stringValue":"3"}}}}},"b":{"expression":{"$case":"stringEqualsExpression","stringEqualsExpression":{"ignoreCase":false,"a":{"expression":{"$case":"bindableString","bindableString":{"key":{"key":{"value":{"id":"employment"}}}}}},"b":{"expression":{"$case":"stringValue","stringValue":"20"}}}}}}}}}}}}}`
	for employment, want := range map[string]bool{"3": false, "20": false, "1": true} {
		t.Run(employment, func(t *testing.T) {
			var expression any
			if err := json.Unmarshal([]byte(nativeExpression), &expression); err != nil {
				t.Fatal(err)
			}
			form := postEditPlan{states: map[string]map[string]any{
				"employment": {"value": map[string]any{"$case": "stringValue", "stringValue": employment}},
				"required":   {"value": map[string]any{"$case": "expression", "expression": expression}},
			}}
			action := map[string]any{"requestedArguments": map[string]any{"requestedStateKeys": []any{map[string]any{"key": map[string]any{"value": map[string]any{"id": "required"}}}}}}
			states, err := form.requestedStates(action)
			if err != nil {
				t.Fatal(err)
			}
			if len(states) != 1 || states[0].(map[string]any)["value"] != want {
				t.Fatalf("employment %s: expected required=%v, got %v", employment, want, states)
			}
		})
	}
}
