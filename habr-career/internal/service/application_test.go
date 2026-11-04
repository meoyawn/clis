package service_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"github.com/meoyawn/clis/habr-career/internal/service"
)

type routeTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r routeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "career.habr.com" {
		return nil, fmt.Errorf("unexpected destination %s", req.URL.Redacted())
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return r.transport.RoundTrip(clone)
}

func writePage(w http.ResponseWriter, state service.Object) {
	w.Header().Set("Content-Type", "text/html")
	b, _ := json.Marshal(state)
	fmt.Fprintf(w, `<html><head><meta name="csrf-token" content="SYNTHETIC_CSRF"></head><body><script type="application/json" data-ssr-state="true">%s</script></body></html>`, b)
}

// Owns the protocol contract: read-only preparation, immutable review inputs,
// exact native wire fields, stateful two-write delivery, independent readback,
// and stopping (not retrying) after partial/uncertain outcomes.
func TestNativeApplicationLifecycle(t *testing.T) {
	for _, scenario := range []string{
		"preview", "send", "missing-approval", "tampered-letter", "changed-resume",
		"changed-employer", "changed-sender", "changed-form", "duplicate", "closed",
		"external-route", "restricted", "unknown-field", "wrong-print", "wrong-role",
		"wrong-account", "wrong-receipt", "wrong-created-readback", "creation-rate-limit",
		"patch-uncertain", "wrong-letter-readback", "leading-space-readback", "trim-final-newline", "auth-redirect",
	} {
		t.Run(scenario, func(t *testing.T) {
			var response service.Object
			requests, posts, patches := 0, 0, 0
			changed := false
			storedLetter := ""
			const id = "1000000000"
			const alias = "synthetic-user"
			const letter = "Здравствуйте! Рассмотрите, пожалуйста, профиль.\nhttps://example.test/work?a=1&b=2\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch r.URL.Path {
				case "/vacancies/" + id:
					if scenario == "auth-redirect" && changed {
						w.Header().Set("Location", "https://foreign.test/login")
						w.WriteHeader(http.StatusFound)
						return
					}
					roleID, companyID, login := 1000000000, 77, alias
					if scenario == "wrong-role" && changed {
						roleID++
					}
					if scenario == "changed-employer" && changed {
						companyID++
					}
					if scenario == "wrong-account" && changed {
						login = "another-user"
					}
					fields := service.Object{"message": service.Object{"value": nil}, "salary": service.Object{"value": nil, "currency": "rur"}}
					if scenario == "changed-form" && changed {
						fields["salary"] = service.Object{"value": 123, "currency": "usd"}
					}
					if scenario == "unknown-field" && changed {
						fields["screening_answer"] = service.Object{"required": true}
					}
					var placeholder, external any
					if scenario == "restricted" && changed {
						placeholder = service.Object{"title": "Account restricted"}
					}
					if scenario == "external-route" && changed {
						external = "https://external.test/apply"
					}
					visibleResponse := response
					if changed && scenario == "duplicate" {
						visibleResponse = service.Object{"id": 12, "author": service.Object{"id": alias}}
					}
					if response != nil {
						visibleResponse = service.Object{"id": response["id"], "author": response["author"], "canUpdate": true, "message": storedLetter}
						if scenario == "wrong-created-readback" {
							visibleResponse["id"] = 999
						}
						if scenario == "wrong-letter-readback" && patches > 0 {
							visibleResponse["message"] = "different saved text"
						}
						if scenario == "leading-space-readback" && patches > 0 {
							visibleResponse["message"] = " " + storedLetter
						}
					}
					writePage(w, service.Object{
						"userLogin": login, "visitor": service.Object{"login": login}, "placeholder": placeholder,
						"vacancy":        service.Object{"id": roleID, "href": "/vacancies/" + id, "title": "Synthetic Architect", "description": "<p>Build observability tools</p>", "archived": scenario == "closed" && changed, "hidden": false, "remoteWork": true, "company": service.Object{"id": companyID, "title": "Synthetic Employer"}, "response": service.Object{"kind": "direct"}, "externalLink": external},
						"createResponse": service.Object{"vacancyId": roleID, "response": visibleResponse, "responsesLeft": 150 - posts, "fields": fields},
					})
				case "/" + alias:
					writePage(w, service.Object{"placeholder": nil, "user": service.Object{"id": alias, "title": "Synthetic Applicant", "isOwner": true, "shouldDisplayInfo": true, "print": service.Object{"html": service.Origin + "/" + alias + "/print"}}, "resume": service.Object{"about": service.Object{"value": "Built production services"}}})
				case "/" + alias + "/print":
					name, text := "Synthetic Applicant", "Built production services with trace-based tests."
					if changed && scenario == "changed-resume" {
						text = "Human changed the profile after review."
					}
					if changed && scenario == "wrong-print" {
						name = "Another Applicant"
					}
					fmt.Fprintf(w, `<div class="content"><h1 class="full_name">%s</h1><a href="%s/%s">Profile</a><p>%s</p></div>`, name, service.Origin, alias, text)
				case "/api/frontend_v1/users/me":
					email := "synthetic@example.test"
					if changed && scenario == "changed-sender" {
						email = "other@example.test"
					}
					_ = json.NewEncoder(w).Encode(service.Object{"user": service.Object{"alias": alias, "email": email, "fullName": "Synthetic Applicant", "gaUidToken": "SYNTHETIC_PRIVATE"}, "meta": service.Object{"logoutToken": "SYNTHETIC_LOGOUT"}})
				case "/api/frontend/vacancies/" + id + "/responses", "/api/frontend/vacancies/" + id + "/responses/8101":
					if r.Header.Get("X-CSRF-Token") != "SYNTHETIC_CSRF" || r.Header.Get("Origin") != service.Origin {
						t.Error("native write lacks CSRF/origin protection")
					}
					if r.Method == http.MethodPost {
						_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
						raw, readErr := io.ReadAll(r.Body)
						if err != nil || readErr != nil || string(raw) != "--"+params["boundary"]+"--\r\n" {
							t.Error("empty creation FormData differs from Chrome's observed no-preamble multipart serialization")
						}
						r.Body = io.NopCloser(bytes.NewReader(raw))
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Errorf("write is not native multipart: %v", err)
					}
					if r.Method == http.MethodPost {
						posts++
						if len(r.MultipartForm.Value) != 0 || response != nil {
							t.Error("creation changed shared profile or duplicated an application")
						}
						if scenario == "creation-rate-limit" {
							w.Header().Set("Retry-After", "120")
							w.WriteHeader(429)
							fmt.Fprint(w, `{"message":"Rate limited SYNTHETIC_CSRF","csrf_token":"SYNTHETIC_CSRF"}`)
							return
						}
						response = service.Object{"id": 8100 + posts, "author": service.Object{"id": alias}, "canUpdate": true}
						receipt := response
						if scenario == "wrong-receipt" {
							receipt = service.Object{"id": 8101, "author": service.Object{"id": "another-user"}}
						}
						_ = json.NewEncoder(w).Encode(service.Object{"response": receipt})
					} else if r.Method == http.MethodPatch {
						patches++
						if response == nil || len(r.MultipartForm.Value) != 1 || len(r.MultipartForm.Value["body"]) != 1 || r.FormValue("body") != letter {
							t.Errorf("letter update changed wrong fields, text or response: %v", r.MultipartForm.Value)
						}
						storedLetter = r.FormValue("body")
						if scenario == "trim-final-newline" {
							storedLetter = strings.TrimRight(storedLetter, "\r\n")
						}
						if scenario == "patch-uncertain" {
							w.WriteHeader(http.StatusInternalServerError)
							fmt.Fprint(w, `{"message":"response interrupted"}`)
							return
						}
						_ = json.NewEncoder(w).Encode(service.Object{"response": response})
					} else {
						t.Errorf("unexpected write method %s", r.Method)
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			httpClient := server.Client()
			httpClient.Transport = routeTransport{target: target, transport: httpClient.Transport}
			jar, _ := cookiejar.New(nil)
			origin, _ := url.Parse(service.Origin)
			jar.SetCookies(origin, []*http.Cookie{{Name: "_career_session", Value: "SYNTHETIC_SESSION"}})
			httpClient.Jar = jar
			client := service.NewClient(httpClient)
			vacancyID, _ := domain.NewVacancyIDFromValue(id)
			resumeID, _ := domain.NewResumeIDFromValue(alias)
			draft, err := client.PrepareApplication(t.Context(), vacancyID, resumeID, letter)
			if err != nil {
				t.Fatal(err)
			}
			if posts != 0 || patches != 0 || draft.SenderEmail != "synthetic@example.test" || draft.Resume.ID != alias || draft.Letter != letter {
				t.Fatalf("preparation wrote or selected the wrong package: %+v", draft)
			}
			encoded, _ := json.Marshal(draft)
			if strings.Contains(string(encoded), "SYNTHETIC_CSRF") || strings.Contains(string(encoded), "SYNTHETIC_PRIVATE") || strings.Contains(string(encoded), "SYNTHETIC_SESSION") {
				t.Fatal("review package exposed credentials")
			}
			if scenario == "preview" {
				return
			}
			changed = true
			approved := draft.ReviewSHA256
			if scenario == "missing-approval" {
				approved = ""
			}
			if scenario == "tampered-letter" {
				draft.Letter += " changed"
			}
			before := requests
			result, err := client.SubmitApplication(t.Context(), *draft, approved)
			if scenario == "send" || scenario == "trim-final-newline" {
				if err != nil || posts != 1 || patches != 1 || result["verified"] != true || result["response_id"] != "8101" || result["letter"] != storedLetter {
					t.Fatalf("delivery not verified: result=%v err=%v posts=%d patches=%d", result, err, posts, patches)
				}
				if scenario == "trim-final-newline" && (result["terminal_newlines_normalized"] != true || result["submitted_letter"] != letter) {
					t.Fatalf("normalization did not retain original sent bytes: %v", result)
				}
				if _, err := client.SubmitApplication(t.Context(), *draft, approved); err == nil || posts != 1 {
					t.Fatal("an existing application was resubmitted")
				}
				return
			}
			if err == nil || result != nil {
				t.Fatalf("unsafe or uncertain outcome reported success: %v", result)
			}
			wantPosts, wantPatches := 0, 0
			switch scenario {
			case "wrong-receipt", "wrong-created-readback", "creation-rate-limit":
				wantPosts = 1
			case "patch-uncertain", "wrong-letter-readback", "leading-space-readback":
				wantPosts, wantPatches = 1, 1
			}
			if posts != wantPosts || patches != wantPatches {
				t.Fatalf("failed guard or automatic retry: %v posts=%d patches=%d", err, posts, patches)
			}
			if (scenario == "missing-approval" || scenario == "tampered-letter") && requests != before {
				t.Fatal("unbound package accessed the network")
			}
			if scenario == "creation-rate-limit" && (!strings.Contains(err.Error(), "120") || strings.Contains(err.Error(), "SYNTHETIC_CSRF")) {
				t.Fatalf("rate-limit diagnostics lost Retry-After or leaked CSRF: %v", err)
			}
			if scenario == "patch-uncertain" {
				readback, err := client.VerifyApplication(t.Context(), *draft)
				if err != nil || readback["verified"] != true || readback["letter"] != letter || readback["response_id"] != "8101" || posts != 1 || patches != 1 {
					t.Fatalf("partial outcome cannot be recovered through readback: %v %v", readback, err)
				}
			}
		})
	}
}
