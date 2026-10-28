package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Synthetic hydration data follows the independently captured native intro
// form. Only the headline may change; location, identity and form settings are
// deliberately present to catch accidental broad edits at the HTTP boundary.
func headlineFormFixture(headline, location string) string {
	values := map[string]any{
		"firstNameIntroForm": "Example", "lastNameIntroForm": "Person",
		"headlineIntroForm": headline, "initialHeadlineIntroForm": headline,
		"locationIntroForm": location, "industryIdIntroForm": "4",
		"showCurrentPositionIntroForm": "Checked",
	}
	payload := map[string]any{"profileId": "ACoExamplePerson", "vanityName": "example-person", "forcePremiumSettingsUpdate": false}
	var states, keys []any
	for field, key := range map[string]string{"firstName": "firstNameIntroForm", "lastName": "lastNameIntroForm", "headline": "headlineIntroForm", "initialHeadline": "initialHeadlineIntroForm", "location": "locationIntroForm", "industry": "industryIdIntroForm", "showCurrentPosition": "showCurrentPositionIntroForm"} {
		protoKey := map[string]any{"key": map[string]any{"value": map[string]any{"$case": "id", "id": key}}}
		keys = append(keys, protoKey)
		states = append(states, map[string]any{"key": map[string]any{"key": protoKey["key"]}, "value": map[string]any{"$case": "stringValue", "stringValue": values[key]}})
		payload[field] = map[string]any{"key": key, "namespace": "MemoryNamespace"}
	}
	row := map[string]any{"modelStates": states, "save": map[string]any{"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{
		"requestId":          "com.linkedin.sdui.requests.profile.saveProfileIntroForm",
		"requestedArguments": map[string]any{"requestedStateKeys": keys, "payload": payload},
	}}}
	data, _ := json.Marshal(row)
	chunks, _ := json.Marshal([]string{"0:" + string(data) + "\n"})
	return `<html><script>window.__como_rehydration__ = ` + string(chunks) + `;</script></html>`
}

func TestHeadlineUpdateUsesFreshOwnFormAndVerifiesOnlyRequestedChange(t *testing.T) {
	const replacement = "Senior Software Engineer | Backend, Developer Tools & AI Agents"
	for _, scenario := range []string{"preview", "apply", "already-present", "wrong-owner", "stale-form", "timeout", "error-receipt", "ignored-save", "changed-location", "missing-visible-headline"} {
		t.Run(scenario, func(t *testing.T) {
			current, location := "Backend engineer", "Existing location"
			if scenario == "already-present" {
				current = replacement
			}
			posts := 0
			client := linkedinClient{session: session{LIAT: "synthetic", JSessionID: "ajax:synthetic"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				client.lastRequestAt = time.Time{}
				switch r.URL.Path {
				case "/voyager/api/me":
					owner := "ACoExamplePerson"
					if scenario == "wrong-owner" {
						owner = "ACoAnotherAccount"
					}
					return jsonResponse(fmt.Appendf(nil, `{"data":{"*miniProfile":"urn:li:fs_miniProfile:%s"}}`, owner)), nil
				case "/voyager/api/identity/dash/profiles":
					var data map[string]any
					if err := json.Unmarshal(fixture(t, "profile"), &data); err != nil {
						t.Fatal(err)
					}
					data["included"].([]any)[1].(map[string]any)["headline"] = current
					encoded, _ := json.Marshal(data)
					return jsonResponse(encoded), nil
				case "/in/example-person/edit/intro/":
					headline := current
					if scenario == "stale-form" {
						headline = "Concurrent edit"
					}
					return jsonResponse([]byte(headlineFormFixture(headline, location))), nil
				case "/in/example-person/":
					visible := current
					if scenario == "missing-visible-headline" {
						visible = "Backend engineer"
					}
					return jsonResponse([]byte(`<html><title>Example Person | LinkedIn</title><main><h1>Example Person</h1><p>` + html.EscapeString(visible) + `</p></main></html>`)), nil
				case "/flagship-web/rsc-action/actions/server-request":
					posts++
					if r.Method != http.MethodPost || r.URL.Query().Get("sduiid") != "com.linkedin.sdui.requests.profile.saveProfileIntroForm" {
						t.Fatalf("wrong headline save endpoint: %s %s", r.Method, r.URL)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					args := body["requestedArguments"].(map[string]any)
					if args["screenId"] != "com.linkedin.sdui.flagshipnav.profile.ProfileEditIntroForm" || !reflect.DeepEqual(body["states"], args["states"]) || jsonStringAt(args, "payload", "profileId") != "ACoExamplePerson" {
						t.Fatal("save lost native owner, screen or consistent states")
					}
					values := map[string]any{}
					for _, item := range body["states"].([]any) {
						state := item.(map[string]any)
						values[state["key"].(string)] = state["value"]
					}
					want := map[string]any{"firstNameIntroForm": "Example", "lastNameIntroForm": "Person", "headlineIntroForm": replacement, "initialHeadlineIntroForm": "Backend engineer", "locationIntroForm": "Existing location", "industryIdIntroForm": "4", "showCurrentPositionIntroForm": "Checked"}
					if !reflect.DeepEqual(values, want) {
						t.Fatalf("save changed more than the headline or lost fields: %v", values)
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
						location = "Unrequested location"
					}
					return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[{"saved":true}]}}}`)), nil
				default:
					return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
			})}
			ref, _ := parseProfile("example-person")
			result, err := client.updateHeadline(context.Background(), ref, replacement, scenario != "preview", t.TempDir())
			wantError := map[string]string{"wrong-owner": "signed-in profile", "stale-form": "differs from current profile", "timeout": "outcome uncertain", "error-receipt": "no confirmed completion", "ignored-save": "not the requested text", "changed-location": "other intro fields changed", "missing-visible-headline": "absent from rendered profile"}[scenario]
			wantPosts := 1
			if scenario == "preview" || scenario == "already-present" || scenario == "wrong-owner" || scenario == "stale-form" {
				wantPosts = 0
			}
			if posts != wantPosts {
				t.Fatalf("got %d writes, want %d", posts, wantPosts)
			}
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("want %q, got %v", wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preview" && (result.Applied || result.Verified) {
				t.Fatal("preview claimed a completed save")
			}
			if scenario == "already-present" && (!result.Verified || !result.AlreadyPresent || result.Applied) {
				t.Fatal("already-present headline was not a verified no-op")
			}
			if scenario == "apply" {
				if !result.Verified || !result.Applied || result.After != replacement {
					t.Fatalf("save was not verified: %+v", result)
				}
				info, err := os.Stat(result.BackupPath)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("private backup missing: %v", err)
				}
			}
		})
	}
}
