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

func TestFullProfilePreservesCareerEvidenceWithinThreeRequests(t *testing.T) {
	overview, err := os.ReadFile("testdata/profile-overview.html")
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
	if calls.Load() != 3 || readers.Load() != 2 || result.Name != "Example Person" || result.FetchedAt == "" || len(result.Coverage) == 0 {
		t.Fatalf("wrong profile identity, provenance, or request budget: calls=%d result=%+v", calls.Load(), result)
	}
	for _, want := range []string{"Angel Investor", "SyntheticBase", "Jan 2025 - Present", "Backed the developer", "2017 - 2021", "first production service", "https://example.org/project", "https://example.org/api", "https://example.org/site", "source_url"} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("profile JSON lost relevant career evidence %q", want)
		}
	}
	for _, unwanted := range []string{"Navigation noise", "Unrelated recruiter", "Unrelated investor", "Private contact overlay", "Unsent private draft", "Tracking metadata", "Not profile content", "Hidden alternate role", "Duplicated inaccessible", "Pending invitation control", "· 1st", "· 2nd", "not-exported"} {
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
