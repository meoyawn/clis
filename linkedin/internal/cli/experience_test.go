package cli

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/meoyawn/clis/linkedin/internal/service"
)

func TestExperienceWriteRefusesAnotherSignedInOwner(t *testing.T) {
	calls := 0
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/voyager/api/me" {
			t.Fatalf("unexpected request before owner validation: %s %s", r.Method, r.URL)
		}
		return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoSomeoneElse"}}`)), nil
	})}}
	ref, err := parseProfile("example-person")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("../service/testdata/experience-save.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	capture, err := service.LoadExperienceCapture(f, "42")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.updateExperience(context.Background(), ref, capture, "Updated text", true, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "signed-in") || calls != 1 {
		t.Fatalf("owner mismatch was not stopped before any profile read/write: calls=%d err=%v", calls, err)
	}
}

func TestPositionDescriptionRequiresExactOwnerAndPosition(t *testing.T) {
	p := fullProfileResult{}
	p.URL = "https://www.linkedin.com/in/example-person/"
	p.Sections = []profileSection{{Title: "Experience", Blocks: []profileBlock{
		{Text: "Other role", Links: []profileLink{{URL: "https://www.linkedin.com/in/another-person/details/experience/edit/forms/42/"}}},
		{Text: "Wrong description"},
		{Text: "Founder", Links: []profileLink{{URL: "https://www.linkedin.com/in/example-person/details/experience/edit/forms/42/"}}},
		{Text: "Correct description"},
	}}}
	got, err := positionDescription(p, "42")
	if err != nil || got != "Correct description" {
		t.Fatalf("selected the wrong role: %q %v", got, err)
	}
	if _, err := positionDescription(p, "99"); err == nil {
		t.Fatal("missing position was accepted")
	}
}

func TestExperienceSaveRequiresFreshDescriptionAndIndependentReadback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		current   string
		persist   bool
		wantPosts int
		wantError string
	}{
		{"verified save", "Current text", true, 1, ""},
		{"concurrent edit", "Changed by another edit", true, 0, "changed since"},
		{"unconfirmed write", "Current text", false, 1, "not verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.Open("../service/testdata/experience-save.json")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			capture, err := service.LoadExperienceCapture(f, "42")
			if err != nil {
				t.Fatal(err)
			}
			current := tc.current
			posts := 0
			client := linkedinClient{session: session{LIAT: "test-token", JSessionID: "ajax:test"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/voyager/api/me":
					return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoExamplePerson"}}`)), nil
				case "/voyager/api/identity/dash/profiles":
					return jsonResponse(fixture(t, "profile")), nil
				case "/in/example-person/":
					return jsonResponse([]byte(`<html><title>Example Person | LinkedIn</title><main><h1>Example Person</h1><p>Profile overview</p></main></html>`)), nil
				case "/in/example-person/details/experience/":
					return jsonResponse([]byte(`<html><title>Example Person | LinkedIn</title><main><h1>Example Person</h1><h2>Experience</h2><a href="/in/example-person/details/experience/edit/forms/42/">Founder</a><p>` + current + `</p></main></html>`)), nil
				case "/flagship-web/rsc-action/actions/server-request":
					if r.Method != "POST" {
						t.Fatalf("save used %s", r.Method)
					}
					posts++
					if tc.persist {
						current = "Website CV text"
					}
					return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[{"saved":true}]}}}`)), nil
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
					return nil, nil
				}
			})}
			ref, err := parseProfile("example-person")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.updateExperience(context.Background(), ref, capture, "Website CV text", true, t.TempDir())
			if posts != tc.wantPosts {
				t.Fatalf("write count=%d, want %d", posts, tc.wantPosts)
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %v", tc.wantError, err)
				}
			} else if err != nil || !result.Applied || !result.Verified {
				t.Fatalf("save was not independently verified: %+v, %v", result, err)
			}
		})
	}
}
