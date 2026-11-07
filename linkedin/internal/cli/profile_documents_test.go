package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFullProfilePreservesCareerAndRelationshipEvidence(t *testing.T) {
	overview, err := os.ReadFile("testdata/profile-overview.html")
	if err != nil {
		t.Fatal(err)
	}
	highlights, err := os.ReadFile("testdata/profile-highlights.rsc")
	if err != nil {
		t.Fatal(err)
	}
	experience, err := os.ReadFile("testdata/profile-experience.html")
	if err != nil {
		t.Fatal(err)
	}
	var calls, readers atomic.Int32
	bothReading := make(chan struct{})
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path == "/flagship-web/rsc-action/actions/component" {
			var request struct {
				ComponentID     string `json:"componentId"`
				ClientArguments struct {
					Payload struct {
						VanityName             string `json:"vanityName"`
						ReplaceableSectionArgs struct {
							VieweeProfileID string `json:"vieweeProfileId"`
						} `json:"replaceableSectionArgs"`
					} `json:"payload"`
				} `json:"clientArguments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				return nil, err
			}
			const component = "com.linkedin.sdui.generated.profile.dsl.impl.profileCardsAboveActivity"
			if r.Method != http.MethodPost || r.URL.Query().Get("componentId") != component || r.URL.Query().Get("sduiid") != component || request.ComponentID != component || request.ClientArguments.Payload.VanityName != "example-person" || request.ClientArguments.Payload.ReplaceableSectionArgs.VieweeProfileID != "ACoExamplePerson" {
				return nil, fmt.Errorf("deferred read lost exact component or target: %s %+v", r.URL, request)
			}
			return jsonResponse(highlights), nil
		}
		if r.Method != http.MethodGet {
			return nil, fmt.Errorf("profile read attempted mutation: %s", r.Method)
		}
		if r.URL.Path == "/voyager/api/identity/dash/profiles" {
			return jsonResponse(fixture(t, "profile")), nil
		}
		if readers.Add(1) == 2 {
			close(bothReading)
		}
		// Neither document can finish before the other starts. A sequential
		// implementation times out here, independently proving real overlap.
		select {
		case <-bothReading:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		switch r.URL.Path {
		case "/in/example-person/":
			return jsonResponse(overview), nil
		case "/in/example-person/details/experience/":
			if r.URL.Query().Get("vieweeProfileId") != "ACoExamplePerson" || r.URL.Query().Get("isSelfProfile") != "false" {
				return nil, fmt.Errorf("experience request lost exact target: %s", r.URL)
			}
			return jsonResponse(experience), nil
		}
		return nil, fmt.Errorf("unnecessary request: %s", r.URL)
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, _, err := client.fullProfile(ctx, profileRef{Slug: "example-person"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 || readers.Load() != 2 || result.Name != "Example Person" || result.FetchedAt == "" {
		t.Fatalf("wrong profile identity, provenance, or request budget: calls=%d result=%+v", calls.Load(), result)
	}
	for _, want := range []string{"Angel Investor", "SyntheticBase", "Jan 2025 - Present", "Backed the developer", "2017 - 2021", "first production service", "https://example.org/project", "https://example.org/api", "https://example.org/site", "source_url", "You both worked at Example Mobility", "Example worked at Example Mobility after you started", "Message with Premium", "https://www.linkedin.com/messaging/compose/"} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("profile JSON lost relevant career evidence %q", want)
		}
	}
	for _, unwanted := range []string{"Navigation noise", "Unrelated recruiter", "Unrelated investor", "Private contact overlay", "Unsent private draft", "Tracking metadata", "Not profile content", "Hidden alternate role", "Duplicated inaccessible", "Pending invitation control", "· 1st", "· 2nd", "not-exported", "Unrelated highlights", "Hidden alternate highlight", "Unsent+suggested+draft", "Suggested+subject"} {
		if strings.Contains(string(encoded), unwanted) {
			t.Errorf("profile JSON included unrelated or unverified content %q", unwanted)
		}
	}
	siteBlocks := 0
	for _, section := range result.Sections {
		for _, block := range section.Blocks {
			if block.Text == "Example site" {
				siteBlocks++
			}
		}
	}
	if siteBlocks != 1 {
		t.Errorf("dynamic tracking links duplicated the same profile evidence %d times", siteBlocks)
	}
}

func TestFullProfileRateLimitCancelsSiblingWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	experienceStarted := make(chan struct{})
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		switch r.URL.Path {
		case "/voyager/api/identity/dash/profiles":
			return jsonResponse(fixture(t, "profile")), nil
		case "/in/example-person/":
			select {
			case <-experienceStarted:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			response := jsonResponse(nil)
			response.StatusCode = http.StatusTooManyRequests
			response.Header.Set("Retry-After", "60")
			return response, nil
		case "/in/example-person/details/experience/":
			close(experienceStarted)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return nil, fmt.Errorf("unexpected request: %s", r.URL)
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, _, err := client.fullProfile(ctx, profileRef{Slug: "example-person"})
	if err == nil || !strings.Contains(err.Error(), "429") || calls.Load() != 3 || result.Name != "" {
		t.Fatalf("rate-limited batch retried or returned success: calls=%d result=%+v err=%v", calls.Load(), result, err)
	}
}

func TestProfileDocumentsRejectWrongIdentityAndMissingContent(t *testing.T) {
	for _, body := range []string{
		`<html><title>Someone Else | LinkedIn</title><main><p>Wrong career</p></main></html>`,
		`<html><title>Example Person | LinkedIn</title><aside>Recommendations only</aside></html>`,
		`<html><title>Example Person | LinkedIn</title><main><script>Placeholder</script></main></html>`,
	} {
		if sections, err := parseProfileDocument(profileDocument{URL: "https://www.linkedin.com/in/example-person/", HTML: body}, "Example Person"); err == nil || len(sections) != 0 {
			t.Fatalf("unconfirmed document returned profile data: %+v, %v", sections, err)
		}
	}
}

func TestProfileHighlightsRejectsForeignEvidenceAndMessageActions(t *testing.T) {
	raw, err := os.ReadFile("testdata/profile-highlights.rsc")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, old, replacement string
		wantError              bool
	}{
		{"wrong target card", "refACoExamplePerson", "refACoSomeoneElse", true},
		{"foreign message recipient", "recipient=ACoExamplePerson", "recipient=ACoSomeoneElse", false},
		{"disabled message", `"buttonProps":{`, `"buttonProps":{"disabled":true,`, false},
		{"unrecognized response", string(raw), "<html>Sign in</html>", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.ReplaceAll(string(raw), tc.old, tc.replacement)
			target := profileResult{person: person{ProfileURN: "urn:li:fsd_profile:ACoExamplePerson", URL: "https://www.linkedin.com/in/example-person/"}}
			section, err := parseProfileHighlights(body, target)
			if tc.wantError {
				if err == nil || section != nil {
					t.Fatalf("unconfirmed response returned shared-history claims: %+v, %v", section, err)
				}
				return
			}
			if err != nil || section == nil || len(section.Blocks) != 2 || section.Blocks[0].Text != "You both worked at Example Mobility" {
				t.Fatalf("shared history lost or unsupported message action returned: %+v, %v", section, err)
			}
		})
	}
}
