package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/meoyawn/clis/getmatch/internal/domain"
)

// Owns the native application protocol: exact letter/answers, no PDF upload,
// a single mutation and independent application-list confirmation.
func TestNativeApplicationContract(t *testing.T) {
	for _, scenario := range []string{"submitted", "PDF draft rejected", "changed letter", "already applied", "write forbidden", "receipt missing", "readback unconfirmed", "list missing", "listed vacancy differs", "listed letter differs", "listed on next page", "masked URL", "wrong compensation"} {
		t.Run(scenario, func(t *testing.T) {
			posts, stateReads := 0, 0
			letter := "Reviewed letter"
			if scenario == "masked URL" {
				letter = "Reviewed letter https://example.com/proof/"
			}
			c := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				status := 200
				var value any
				switch r.URL.Path {
				case "/api/offers/123":
					value = Object{"id": 123, "url": "/vacancies/123-go-developer", "position": "Go developer", "is_active": true, "offer_type": "vacancy", "company": Object{"name": "Synthetic Co"}}
				case "/api/auth/me":
					stateReads++
					if r.URL.Query().Get("vacancy_id") != "123" {
						t.Fatal("status not bound to vacancy")
					}
					value = Object{"id": "candidate", "already_applied": scenario == "already applied" || posts > 0 && scenario != "readback unconfirmed", "verified": true}
				case "/api/profiles":
					value = Object{"id": 42, "first_name": "Test", "experience": []any{}}
				case "/api/applications/candidate":
					rows := []any{}
					listed := Object{"id": 789, "status": "new", "applied_at": "2026-10-28T12:00:00Z", "salary_min": 470000, "cover_letter": letter, "vacancy": Object{"url": "/vacancies/123-go-developer", "position": "Go developer", "company": Object{"name": "Synthetic Co"}}}
					if scenario == "masked URL" {
						listed["cover_letter"] = "Reviewed letter •••••"
					}
					if scenario == "wrong compensation" {
						listed["salary_min"] = 47000
					}
					if scenario == "listed vacancy differs" {
						listed["vacancy"].(Object)["url"] = "/vacancies/999-different"
					}
					if scenario == "listed letter differs" {
						listed["cover_letter"] = "Different letter"
					}
					total, offset := 1, 0
					if scenario == "list missing" {
						total = 0
					} else if scenario == "listed on next page" && r.URL.Query().Get("offset") == "0" {
						for i := range 20 {
							rows = append(rows, Object{"id": i + 1000})
						}
						total = 21
					} else {
						rows = append(rows, listed)
						if scenario == "listed on next page" {
							total, offset = 21, 20
						}
					}
					value = Object{"applications": rows, "meta": Object{"total": total, "offset": offset, "limit": 20}}
				case "/api/offers/123/apply":
					posts++
					if r.Method != http.MethodPost || r.Header.Get("Origin") != Origin {
						t.Fatal("wrong application request")
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := r.MultipartForm.RemoveAll(); err != nil {
							t.Error(err)
						}
					}()
					if _, _, err := r.FormFile("cv"); err != http.ErrMissingFile {
						t.Fatal("native application unexpectedly includes a CV upload")
					}
					if r.FormValue("cover_letter") != letter || r.FormValue("salary_from") != "470000" || r.FormValue("salary_currency") != "RUB" || r.FormValue("location_id") != "city" || r.FormValue("first_name") != "Test" || r.FormValue("last_name") != "Applicant" {
						t.Fatal("reviewed native answers changed in transport")
					}
					if scenario == "write forbidden" {
						status = 403
						value = Object{"private": "PRIVATE_SENTINEL"}
					} else if scenario == "receipt missing" {
						value = Object{"ok": true}
					} else {
						value = Object{"application_id": 789}
					}
				default:
					t.Fatalf("unexpected endpoint %s", r.URL)
				}
				data, _ := json.Marshal(value)
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}, nil)
			id, _ := domain.NewVacancyIDFromValue("123")
			draft, err := c.PrepareApplication(context.Background(), id, "", letter, ApplicationAnswers{FirstName: "Test", LastName: "Applicant", SalaryFrom: 470000, SalaryCurrency: "RUB", LocationID: "city"})
			if scenario == "already applied" {
				if err == nil || posts != 0 {
					t.Fatal("duplicate was not stopped during preflight")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			hash := draft.ReviewSHA256
			if scenario == "PDF draft rejected" {
				draft.CVPath = "/synthetic/legacy-cv.pdf"
				draft.ReviewSHA256 = draftDigest(*draft)
				hash = draft.ReviewSHA256
			}
			if scenario == "changed letter" {
				draft.CoverLetter = "Unreviewed change"
			}
			receipt, err := c.SubmitApplication(context.Background(), *draft, hash)
			switch scenario {
			case "PDF draft rejected", "changed letter":
				if err == nil || posts != 0 {
					t.Fatal("changed package reached POST")
				}
			case "write forbidden", "receipt missing":
				if err == nil || posts != 1 || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
					t.Fatalf("uncertain write retried or leaked: posts=%d err=%v", posts, err)
				}
			default:
				if err != nil || posts != 1 || stateReads != 3 || receipt["verified"] != (scenario == "submitted" || scenario == "listed on next page" || scenario == "masked URL") {
					t.Fatalf("receipt/readback mismatch: posts=%d states=%d result=%v err=%v", posts, stateReads, receipt, err)
				}
			}
		})
	}
}
