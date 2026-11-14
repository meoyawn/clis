package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func skillFormFixture(count int) string {
	payload := map[string]any{"profileId": "ACoExamplePerson", "vanityName": "example-person", "isEdit": false, "totalSkills": count}
	var states, keys []any
	for field, value := range map[string]any{"skillName": "", "skillId": "", "followToKeepUp": false, "experienceAssociations": []string{}} {
		kind := "stringValue"
		if field == "followToKeepUp" {
			kind = "booleanValue"
		}
		if field == "experienceAssociations" {
			kind, value = "stringListValue", map[string]any{"stringValues": value}
		}
		key := map[string]any{"key": map[string]any{"value": map[string]any{"id": field}}}
		keys = append(keys, key)
		states = append(states, map[string]any{"key": map[string]any{"key": key["key"]}, "value": map[string]any{"$case": kind, kind: value}})
		payload[field] = map[string]any{"key": field, "namespace": "MemoryNamespace"}
	}
	action := func(id string) map[string]any {
		return map[string]any{"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{"requestId": id, "requestedArguments": map[string]any{"requestedStateKeys": keys, "payload": payload}}}
	}
	return skillDocumentFixture(map[string]any{"modelStates": states, "save": action("com.linkedin.sdui.requests.profile.saveProfileSkillForm"), "typeahead": action("PROFILE_ADD_SKILLS_FORM_TYPEAHEAD_REQUEST")})
}

func skillDocumentFixture(row any) string {
	data, _ := json.Marshal(row)
	chunks, _ := json.Marshal([]string{"0:" + string(data) + "\n"})
	return `<html><script>window.__como_rehydration__ = ` + string(chunks) + `;</script></html>`
}

func skillItemFixture(name, id string) map[string]any {
	return map[string]any{"buttonProps": map[string]any{"aria-label": "Edit " + name + " skill"}, "triggers": []any{map[string]any{"action": map[string]any{"actions": []any{map[string]any{"$type": "proto.sdui.actions.core.Navigate", "value": map[string]any{"content": map[string]any{"screen": map[string]any{"url": "/in/example-person/details/skills/edit/forms/" + id + "/"}}}}}}}}}
}

func TestNativeSkillAddVerifiesPaginatedInventoryAndNeverRetries(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "existing-on-next-page", "wrong-owner", "timeout", "missing-readback", "changed-existing-skill"} {
		t.Run(scenario, func(t *testing.T) {
			inventory := []map[string]any{skillItemFixture("Go", "11"), skillItemFixture("Kotlin", "12")}
			if scenario == "existing-on-next-page" {
				inventory[1] = skillItemFixture("Redis", "12")
			}
			saves, pages := 0, 0
			client := linkedinClient{session: session{LIAT: "synthetic", JSessionID: "ajax:synthetic"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				client.lastRequestAt = time.Time{}
				switch r.URL.Path {
				case "/voyager/api/me":
					owner := "ACoExamplePerson"
					if scenario == "wrong-owner" {
						owner = "ACoOtherPerson"
					}
					return jsonResponse(fmt.Appendf(nil, `{"data":{"*miniProfile":"urn:li:fs_miniProfile:%s"}}`, owner)), nil
				case "/voyager/api/identity/dash/profiles":
					return jsonResponse(fixture(t, "profile")), nil
				case "/in/example-person/skills/edit/forms/new/":
					return jsonResponse([]byte(skillFormFixture(len(inventory)))), nil
				case "/in/example-person/edit/intro/":
					return jsonResponse([]byte(headlineFormFixture("Backend engineer", "Test City"))), nil
				case "/in/example-person/details/skills/":
					pager := map[string]any{"$type": "proto.sdui.actions.requests.PaginationRequest", "pagerId": "com.linkedin.sdui.pagers.profile.details.skills", "requestedArguments": map[string]any{"requestedStateKeys": []any{}, "payload": map[string]any{"profileId": "ACoExamplePerson", "vanityName": "example-person", "filter": "ProfileSkillCategory_ALL", "start": 0, "count": 10}}}
					return jsonResponse([]byte(skillDocumentFixture(map[string]any{"item": inventory[0], "pager": pager}))), nil
				case "/flagship-web/rsc-action/actions/pagination":
					pages++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					args := body["clientArguments"].(map[string]any)
					payload := args["payload"].(map[string]any)
					start := int(payload["start"].(float64))
					if r.Method != http.MethodPost || start != 1 || payload["profileId"] != "ACoExamplePerson" || body["pagerId"] != "com.linkedin.sdui.pagers.profile.details.skills" {
						t.Fatalf("wrong skill-page request: %v", body)
					}
					data, _ := json.Marshal(inventory[start:])
					return jsonResponse(append([]byte("0:"), data...)), nil
				case "/flagship-web/rsc-action/actions/server-request":
					if r.URL.Query().Get("sduiid") == "PROFILE_ADD_SKILLS_FORM_TYPEAHEAD_REQUEST" {
						set := func(key, value string) map[string]any {
							return map[string]any{"$type": "proto.sdui.actions.core.SetState", "value": map[string]any{"state": map[string]any{"key": map[string]any{"key": map[string]any{"value": map[string]any{"id": key}}}, "value": map[string]any{"stringValue": value}}}}
						}
						row := map[string]any{"response": map[string]any{"errors": []any{}, "completionAction": map[string]any{"actions": []any{map[string]any{"actions": []any{set("skillName", "Redis"), set("skillId", "999")}}}}}}
						data, _ := json.Marshal(row)
						return jsonResponse(append([]byte("0:"), data...)), nil
					}
					saves++
					if r.URL.Query().Get("sduiid") != "com.linkedin.sdui.requests.profile.saveProfileSkillForm" {
						t.Fatalf("unexpected mutation: %s", r.URL)
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
					associations, ok := values["experienceAssociations"].([]any)
					if values["skillName"] != "Redis" || values["skillId"] != "999" || values["followToKeepUp"] != false || !ok || len(associations) != 0 {
						t.Fatalf("native save changed the requested skill or followed/associated it: %v", values)
					}
					if scenario == "timeout" {
						return nil, context.DeadlineExceeded
					}
					if scenario != "missing-readback" {
						inventory = append(inventory, skillItemFixture("Redis", "33"))
					}
					if scenario == "changed-existing-skill" {
						inventory[1] = skillItemFixture("Python", "12")
					}
					return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[{"saved":true}]}}}`)), nil
				default:
					return nil, fmt.Errorf("unexpected request %s", r.URL)
				}
			})}
			ref, _ := parseProfile("example-person")
			result, err := client.addProfileSkill(context.Background(), ref, "Redis", scenario != "preview", t.TempDir())
			wantError := map[string]string{"wrong-owner": "signed-in owner", "timeout": "outcome uncertain", "missing-readback": "did not match inventory", "changed-existing-skill": "existing skill"}[scenario]
			wantSaves := 1
			if scenario == "preview" || scenario == "existing-on-next-page" || scenario == "wrong-owner" {
				wantSaves = 0
			}
			if saves != wantSaves {
				t.Fatalf("got %d saves, want %d; retries or duplicate writes are forbidden", saves, wantSaves)
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
			if pages == 0 {
				t.Fatal("inventory was accepted without reading its second page")
			}
			if scenario == "apply" {
				info, err := os.Stat(result.RecordPath)
				if !result.Applied || !result.Verified || err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("saved skill not verified or private backup missing: %+v, %v", result, err)
				}
			}
			if scenario == "preview" && (result.Applied || result.Verified) {
				t.Fatal("preview claimed a saved skill")
			}
		})
	}
}
