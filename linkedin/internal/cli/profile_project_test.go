package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func projectFormFixture(fields map[string]any) string {
	defaults := map[string]any{"name": "", "description": "", "currentlyWorking": "Unchecked", "associateId": "", "startDate": map[string]any{"$type": "proto.sdui.common.Date", "day": 0, "month": 0, "year": 0}, "endDate": map[string]any{"$type": "proto.sdui.common.Date", "day": 0, "month": 0, "year": 0}}
	for k, v := range fields {
		defaults[k] = v
	}
	var states []any
	for field, value := range defaults {
		key, kind := "syntheticProjectForm"+field, "stringValue"
		if field == "startDate" || field == "endDate" {
			key, kind = "ProjectForm"+field, "dateValue"
		}
		states = append(states, map[string]any{"key": map[string]any{"key": map[string]any{"value": map[string]any{"id": key}}}, "value": map[string]any{"$case": kind, kind: value}})
	}
	row := map[string]any{"modelStates": states, "save": map[string]any{"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{
		"requestId": "com.linkedin.sdui.impl.profile.components.forms.uploadProjectMedia", "requestedArguments": map[string]any{"requestedStateKeys": []any{}, "payload": map[string]any{"profileId": "ACoExamplePerson", "vanityName": "example-person", "formIdPrefix": "syntheticProjectForm", "existingMedia": []any{}}},
	}}}
	data, _ := json.Marshal(row)
	chunks, _ := json.Marshal([]string{"0:" + string(data) + "\n"})
	return `<html><script>window.__como_rehydration__ = ` + string(chunks) + `;</script></html>`
}

func TestProjectCreateUsesNativeDatesAndRequiresANewVerifiedProject(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "duplicate", "wrong-save-owner", "wrong-date", "missing-readback"} {
		t.Run(scenario, func(t *testing.T) {
			draft := profileProjectDraft{Name: "Example experiment", Description: "Published experiment with a linked report.", Start: "2026-08", End: "2026-09"}
			changes, err := draft.fields()
			if err != nil {
				t.Fatal(err)
			}
			created, posts := false, 0
			client := linkedinClient{session: session{LIAT: "synthetic", JSessionID: "ajax:synthetic"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				client.lastRequestAt = time.Time{}
				switch r.URL.Path {
				case "/voyager/api/me":
					return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoExamplePerson"}}`)), nil
				case "/voyager/api/identity/dash/profiles":
					return jsonResponse(fixture(t, "profile")), nil
				case "/in/example-person/details/projects/":
					body := `<h2>Projects</h2><h3>Existing project</h3><a href="/in/example-person/details/projects/edit/forms/42/">Edit</a>`
					if scenario == "duplicate" || (created && scenario != "missing-readback") {
						body += `<h3>Example experiment</h3><a href="/in/example-person/details/projects/edit/forms/77/">Edit</a>`
					}
					return jsonResponse([]byte(`<html><title>Example Person | LinkedIn</title><main>` + body + `</main></html>`)), nil
				case "/in/example-person/edit/forms/project/new/":
					return jsonResponse([]byte(projectFormFixture(nil))), nil
				case "/in/example-person/details/projects/edit/forms/77/":
					fields := map[string]any{}
					for k, v := range changes {
						fields[k] = v
					}
					if scenario == "wrong-date" {
						fields["startDate"] = map[string]any{"$type": "proto.sdui.common.Date", "day": 0, "month": 9, "year": 2026}
					}
					return jsonResponse([]byte(projectFormFixture(fields))), nil
				case "/flagship-web/rsc-action/actions/server-request":
					posts++
					if posts == 1 {
						if r.URL.Query().Get("sduiid") != "com.linkedin.sdui.impl.profile.components.forms.uploadProjectMedia" {
							t.Fatal("project did not prepare its native save")
						}
						owner := "ACoExamplePerson"
						if scenario == "wrong-save-owner" {
							owner = "ACoAnotherPerson"
						}
						var keys []any
						for _, key := range []string{"syntheticProjectFormname", "syntheticProjectFormdescription", "ProjectFormstartDate", "ProjectFormendDate", "syntheticProjectFormcurrentlyWorking", "syntheticProjectFormassociateId"} {
							keys = append(keys, map[string]any{"key": map[string]any{"value": map[string]any{"id": key}}})
						}
						response := map[string]any{"response": map[string]any{"errors": []any{}, "completionAction": map[string]any{"actions": []any{map[string]any{"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{
							"requestId": "com.linkedin.sdui.requests.profile.saveProfileProjectForm", "requestedArguments": map[string]any{"requestedStateKeys": keys, "payload": map[string]any{"profileId": owner, "vanityName": "example-person"}},
						}}}}}}
						data, _ := json.Marshal(response)
						return jsonResponse(append([]byte("0:"), data...)), nil
					}
					if posts != 2 || r.URL.Query().Get("sduiid") != "com.linkedin.sdui.requests.profile.saveProfileProjectForm" {
						t.Fatal("unexpected save request or retry")
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
					want := map[string]any{"syntheticProjectFormname": "Example experiment", "syntheticProjectFormdescription": "Published experiment with a linked report.", "syntheticProjectFormcurrentlyWorking": "Unchecked", "syntheticProjectFormassociateId": "", "ProjectFormstartDate": map[string]any{"$type": "proto.sdui.common.Date", "day": float64(0), "month": float64(8), "year": float64(2026)}, "ProjectFormendDate": map[string]any{"$type": "proto.sdui.common.Date", "day": float64(0), "month": float64(9), "year": float64(2026)}}
					if !reflect.DeepEqual(values, want) {
						t.Fatalf("native save lost project fields or dates: %v", values)
					}
					created = true
					return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[{"saved":true}]}}}`)), nil
				default:
					return nil, fmt.Errorf("unexpected request %s", r.URL)
				}
			})}
			ref, _ := parseProfile("example-person")
			result, err := client.addProfileProject(context.Background(), ref, changes, scenario != "preview", t.TempDir())
			wantError := map[string]string{"duplicate": "already exists", "wrong-save-owner": "signed-in profile", "wrong-date": "readback differs", "missing-readback": "exactly one new project"}[scenario]
			wantPosts := 2
			if scenario == "preview" || scenario == "duplicate" {
				wantPosts = 0
			} else if scenario == "wrong-save-owner" {
				wantPosts = 1
			}
			if posts != wantPosts {
				t.Fatalf("got %d requests, want %d; error: %v", posts, wantPosts, err)
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
			if result.Verified != (scenario == "apply") || result.Applied != (scenario == "apply") {
				t.Fatalf("incorrect creation status: %+v", result)
			}
		})
	}
}
