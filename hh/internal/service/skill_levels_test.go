package service_test

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

func TestSharedSkillLevelsUpdateUsesNativeContractAndChecksPrintViews(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "stale-print", "changed-other-level"} {
		t.Run(scenario, func(t *testing.T) {
			backendHash, otherHash := strings.Repeat("a", 38), strings.Repeat("b", 38)
			levels := map[string]int{"Stripe": 0, "Kafka": 0, "Go": 10}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/shards/applicant/resumes":
					_ = json.NewEncoder(w).Encode(hh.Object{"resumes": []any{hh.Object{"_attributes": hh.Object{"hash": backendHash}}, hh.Object{"_attributes": hh.Object{"hash": otherHash}}}})
				case "/shards/applicant/profile/get_full_data":
					_ = json.NewEncoder(w).Encode(hh.Object{"profile": hh.Object{"fields": hh.Object{"firstName": []any{hh.Object{"string": "Synthetic"}}}}})
				case "/applicant/resume":
					hash := r.URL.Query().Get("resume")
					tags := []any{hh.Object{"string": "Stripe"}, hh.Object{"string": "Go"}}
					if hash == backendHash {
						tags = append(tags, hh.Object{"string": "Kafka"})
					} else if hash != otherHash {
						t.Fatalf("unexpected resume %q", hash)
					}
					inventory := []any{}
					for _, skill := range []struct {
						name string
						id   int
					}{{"Stripe", 11}, {"Kafka", 12}, {"Go", 13}} {
						var level any
						if levels[skill.name] != 0 {
							level = hh.Object{"id": levels[skill.name], "internalId": map[int]string{8: "base", 9: "middle", 10: "advanced"}[levels[skill.name]], "name": map[int]string{8: "Базовый", 9: "Средний", 10: "Продвинутый"}[levels[skill.name]]}
						}
						inventory = append(inventory, hh.Object{"id": skill.id, "name": skill.name, "category": "SKILL", "level": level})
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"_attributes": hh.Object{"hash": hash}, "keySkills": tags, "resumeApplicantSkills": inventory})
				case "/shards/applicant/key_skills/levels/update":
					writes++
					if r.Method != http.MethodPut || r.Header.Get("X-Xsrftoken") != "fixture-token" || r.URL.RawQuery != "" {
						t.Errorf("skill levels lost native request contract: %s %s", r.Method, r.URL)
					}
					var body struct {
						UserSkillLevels []struct {
							Category string `json:"category"`
							LevelID  int    `json:"level_id"`
							SkillID  int    `json:"skill_id"`
						} `json:"userSkillLevels"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode skill-level write: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if len(body.UserSkillLevels) != 2 || body.UserSkillLevels[0].Category != "SKILL" || body.UserSkillLevels[0].SkillID != 11 || body.UserSkillLevels[0].LevelID != 10 || body.UserSkillLevels[1].Category != "SKILL" || body.UserSkillLevels[1].SkillID != 12 || body.UserSkillLevels[1].LevelID != 9 {
						t.Errorf("unexpected skill-level write: %+v", body.UserSkillLevels)
					}
					levels["Stripe"], levels["Kafka"] = 10, 9
					if scenario == "changed-other-level" {
						levels["Go"] = 8
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					if !strings.HasPrefix(r.URL.Path, "/resume/") {
						http.NotFound(w, r)
						return
					}
					hash := strings.TrimPrefix(r.URL.Path, "/resume/")
					printedStripe := levels["Stripe"]
					if scenario == "stale-print" && writes > 0 && hash == otherHash {
						printedStripe = 0
					}
					fmt.Fprint(w, `<div id="HH-React-Root"><h4>Навыки</h4><div>Продвинутый уровень</div>`)
					if printedStripe == 10 {
						fmt.Fprint(w, `<div>Stripe</div>`)
					}
					fmt.Fprint(w, `<div>Go</div><div>Средний уровень</div>`)
					if hash == backendHash && levels["Kafka"] == 9 {
						fmt.Fprint(w, `<div>Kafka</div>`)
					}
					fmt.Fprint(w, `<div>Уровень не указан</div>`)
					if printedStripe == 0 {
						fmt.Fprint(w, `<div>Stripe</div>`)
					}
					fmt.Fprint(w, `<h4>Языки</h4></div>`)
					fmt.Fprintf(w, `<template id="HH-Lux-InitialState">%s</template>`, html.EscapeString(fmt.Sprintf(`{"resume":{"hash":%q}}`, hash)))
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(base, []*http.Cookie{{Name: "_xsrf", Value: "fixture-token"}})
			httpClient := server.Client()
			httpClient.Jar = jar
			client := hh.NewClient(httpClient, base)
			id, _ := hh.NewResumeIDFromValue(backendHash)
			patch := hh.Object{"levels": []any{hh.Object{"name": "Stripe", "level_id": 10}, hh.Object{"name": "Kafka", "level_id": 9}}}
			result, err := client.UpdateSkillLevels(t.Context(), id, patch, scenario != "preview", t.TempDir())
			if scenario == "preview" {
				if err != nil || writes != 0 || result["applied"] != false {
					t.Fatalf("preview wrote or failed: %v %v writes=%d", result, err, writes)
				}
				return
			}
			if scenario != "apply" {
				if err == nil || writes != 1 {
					t.Fatalf("unsafe skill-level update succeeded or retried: %v %v writes=%d", result, err, writes)
				}
				return
			}
			if err != nil || writes != 1 || result["verified"] != true || len(result["affected_resumes"].([]any)) != 2 {
				t.Fatalf("shared levels were not verified in both print views: %v %v writes=%d", result, err, writes)
			}
			info, err := os.Stat(result["backup_path"].(string))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("private backup missing: %v %v", info, err)
			}
		})
	}
}
