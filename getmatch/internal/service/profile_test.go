package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
)

// This boundary owns native shared-profile edits: omitted personal fields must
// survive, employment replacement must remove only selected rows, and a real
// readback must detect a provider-side alteration rather than trust HTTP 200.
func TestProfileUpdatePreservesUneditedFieldsAndChecksReadback(t *testing.T) {
	for _, scenario := range []string{"preview", "saved", "provider changes phone"} {
		t.Run(scenario, func(t *testing.T) {
			state := Object{"id": float64(42), "first_name": "Test", "last_name": "Applicant", "email": "test@example.com", "phone": float64(1234567), "summary": "Old summary", "skills": []any{}, "languages": []any{}, "educations": []any{}, "experience": []any{
				Object{"id": float64(1), "position": "Engineer", "summary": "Real employment", "from_year": float64(2020), "from_month": float64(1), "to_month": nil, "to_year": nil, "skills": []any{}, "company_name": "Synthetic Co", "company_url": "https://example.com", "company_id": nil},
				Object{"id": float64(2), "position": "Personal prototype", "summary": "Incorrectly parsed project", "from_year": float64(2026)},
			}}
			retained := Object{"id": float64(1), "position": "Engineer", "summary": "Translated real employment", "from_year": float64(2020), "from_month": float64(1), "to_month": nil, "to_year": nil, "skills": []any{}, "company_name": "Synthetic Co", "company_url": "https://example.com", "company_id": nil}
			patch := Object{"summary": "General profile with separately labelled personal projects", "languages": []any{Object{"language": "Английский", "level_id": float64(5)}}, "experience": []any{retained}}
			writes := 0
			c := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPut {
					writes++
					var body Object
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					switch r.URL.Path {
					case "/api/profiles/summary":
						if body["email"] != "test@example.com" || body["phone"] != float64(1234567) || body["first_name"] != "Test" || body["summary"] != patch["summary"] {
							t.Fatal("summary edit lost the unedited personal fields")
						}
						state["summary"] = body["summary"]
						state["languages"] = []any{Object{"language": "Английский", "level": Object{"id": float64(5)}}}
						if scenario == "provider changes phone" {
							state["phone"] = float64(999)
						}
					case "/api/profiles/experience":
						if body["profile_id"] != float64(42) || len(body["experience"].([]any)) != 1 {
							t.Fatal("replacement not scoped to the selected profile and real employer")
						}
						state["experience"] = body["experience"]
					default:
						t.Fatalf("unexpected profile mutation %s", r.URL)
					}
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
				}
				if r.Method != http.MethodGet || r.URL.Path != "/api/profiles" {
					t.Fatalf("unexpected profile request %s %s", r.Method, r.URL)
				}
				data, _ := json.Marshal(state)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}, nil)
			result, err := c.UpdateProfile(context.Background(), patch, scenario != "preview", t.TempDir())
			if scenario == "provider changes phone" {
				if err == nil || writes != 2 {
					t.Fatalf("provider change was trusted or write retried: writes=%d err=%v", writes, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preview" {
				if writes != 0 || result["applied"] != false {
					t.Fatal("preview mutated the live profile")
				}
				return
			}
			if writes != 2 || result["verified"] != true || len(state["experience"].([]any)) != 1 {
				t.Fatalf("native update not verified: writes=%d result=%v", writes, result)
			}
			backup, err := os.ReadFile(result["backup_path"].(string))
			if err != nil || !bytes.Contains(backup, []byte("Incorrectly parsed project")) {
				t.Fatal("original shared profile was not preserved before replacement")
			}
		})
	}
}
