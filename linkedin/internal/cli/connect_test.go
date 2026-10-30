package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Synthetic controls preserve the captured member-bound component keys and
// labels. Serialized alternatives must not be treated as rendered controls.
const connectControl = `<a aria-disabled="false" href="/preload/custom-invite/?vanityName=example-person" componentkey="ConnectButtonstate:invitation:urn:li:member:123456_connect" aria-label="Invite Example Person to connect">Connect</a>`
const pendingControl = `<a aria-disabled="false" href="https://www.linkedin.com/" componentkey="ConnectButtonstate:invitation:urn:li:member:123456_pending" aria-label="Pending, click to withdraw invitation sent to Example Person">Pending</a>`

func connectionDocument(controls string) string {
	return `<html><head><title>Example Person | LinkedIn</title></head><body><main><section aria-label="Primary content"><h2>Example Person</h2>` + controls +
		`<script type="application/json">{"unused":"` + pendingControl + `"}</script></section></main></body></html>`
}

// The More menu serializes every alternative. Only modelStates selects the
// current invitation state; finding a dormant Connect action is insufficient.
const connectionMenuFlight = `1:{"modelStates":[{"key":{"key":{"value":{"id":"state:invitation:urn:li:member:123456"}}},"value":{"$case":"stringValue","stringValue":"Connect"}}]}
2:{"viewTrackingSpecs":{"viewName":"profile-overflow-button"},"children":{"triggers":[{"action":{"actions":[{"$type":"proto.sdui.actions.core.ShowMenu","value":{"component":{"role":"menu","children":{"selectedStateKey":{"key":{"value":{"id":"state:invitation:urn:li:member:123456"}}},"states":"$Q3"}}}}]}}]}}
3:[["Connect","$L4"],["Pending","$L5"]]
4:{"children":{"role":"menuitem","componentkey":"ConnectButtonstate:invitation:urn:li:member:123456_connect","triggers":[{"action":{"actionLabel":"Invite Example Person to connect","actions":[{"$type":"proto.sdui.actions.core.Navigate","value":{"content":{"url":{"urlValue":{"url":"/preload/custom-invite/?vanityName=example-person"}}}}}]}}]}}
5:{"children":{"role":"menuitem","componentkey":"ConnectButtonstate:invitation:urn:li:member:123456_pending","triggers":[{"action":{"actionLabel":"Pending, click to withdraw invitation sent to Example Person"}}]}}
`

func connectionMenuScript(t *testing.T, flight string) string {
	t.Helper()
	payload, err := json.Marshal([]string{flight})
	if err != nil {
		t.Fatal(err)
	}
	return "<script>window.__como_rehydration__ = " + string(payload) + "</script>"
}

func pendingMenuFlight() string {
	return strings.Replace(connectionMenuFlight, `"stringValue":"Connect"`, `"stringValue":"Pending"`, 1)
}

func TestConnectPreviewSendAndUncertainOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		send        bool
		before      string
		after       string
		postStatus  int
		postError   bool
		readStatus  int
		wantPosts   int
		wantReads   int
		wantSent    bool
		wantCan     bool
		wantPending bool
		wantError   bool
	}{
		{name: "preview is read only", before: connectControl, wantReads: 1, wantCan: true},
		{name: "pending preview", before: pendingControl, wantReads: 1, wantPending: true},
		{name: "pending send is a no-op", send: true, before: pendingControl, wantReads: 1, wantPending: true},
		{name: "send confirmed by pending", send: true, before: connectControl, after: pendingControl, wantPosts: 1, wantReads: 2, wantSent: true, wantPending: true},
		{name: "More menu preview", before: connectionMenuScript(t, connectionMenuFlight), wantReads: 1, wantCan: true},
		{name: "More menu pending is a no-op", send: true, before: connectionMenuScript(t, pendingMenuFlight()), wantReads: 1, wantPending: true},
		{name: "More menu send confirmed by pending", send: true, before: connectionMenuScript(t, connectionMenuFlight), after: connectionMenuScript(t, pendingMenuFlight()), wantPosts: 1, wantReads: 2, wantSent: true, wantPending: true},
		{name: "HTTP success is not confirmation", send: true, before: connectControl, after: connectControl, wantPosts: 1, wantReads: 2, wantError: true},
		{name: "readback cannot confirm a different member", send: true, before: connectControl, after: strings.ReplaceAll(pendingControl, "123456", "999999"), wantPosts: 1, wantReads: 2, wantError: true},
		{name: "transport failure is not retried", send: true, before: connectControl, postError: true, wantPosts: 1, wantReads: 1, wantError: true},
		{name: "quota rejection stops", send: true, before: connectControl, postStatus: 429, wantPosts: 1, wantReads: 1, wantError: true},
		{name: "authentication rejection stops", send: true, before: connectControl, postStatus: 403, wantPosts: 1, wantReads: 1, wantError: true},
		{name: "readback restriction stays unconfirmed", send: true, before: connectControl, readStatus: 429, wantPosts: 1, wantReads: 2, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts, reads int
			client := connectionTestClient(t, func() (*http.Response, error) {
				reads++
				if reads == 1 {
					return jsonResponse([]byte(connectionDocument(tc.before))), nil
				}
				response := jsonResponse([]byte(connectionDocument(tc.after)))
				if tc.readStatus != 0 {
					response.StatusCode = tc.readStatus
				}
				return response, nil
			}, func(r *http.Request) (*http.Response, error) {
				posts++
				if !tc.send {
					t.Fatal("preview sent a connection request")
				}
				if r.URL.Path != "/voyager/api/voyagerRelationshipsDashMemberRelationships" ||
					r.URL.Query().Get("action") != "verifyQuotaAndCreateV2" ||
					r.URL.Query().Get("decorationId") != "com.linkedin.voyager.dash.deco.relationships.InvitationCreationResultWithInvitee-3" || len(r.URL.Query()) != 2 {
					t.Fatalf("unexpected invitation endpoint: %s", r.URL)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(payload)
				if err != nil || string(encoded) != `{"invitee":{"inviteeUnion":{"memberProfile":"urn:li:fsd_profile:ACoExamplePerson"}}}` {
					t.Fatalf("wrong target or unexpected note/tracking fields: %s; %v", body, err)
				}
				if r.Header.Get("Content-Type") != "application/json; charset=UTF-8" || r.Header.Get("Accept") != "application/vnd.linkedin.normalized+json+2.1" {
					t.Fatalf("wrong invitation content negotiation: %v", r.Header)
				}
				if tc.postError {
					return nil, errors.New("response lost")
				}
				response := jsonResponse([]byte(`{"data":{}}`))
				if tc.postStatus != 0 {
					response.StatusCode = tc.postStatus
				}
				return response, nil
			})
			result, err := client.connect(context.Background(), profileRef{Slug: "example-person"}, tc.send)
			if posts != tc.wantPosts || reads != tc.wantReads {
				t.Fatalf("requests: posts=%d reads=%d; wanted %d/%d; error=%v", posts, reads, tc.wantPosts, tc.wantReads, err)
			}
			if tc.wantError {
				if err == nil || result.Sent {
					t.Fatalf("unconfirmed invitation reported success: %+v; %v", result, err)
				}
				return
			}
			if err != nil || result.Sent != tc.wantSent || result.CanConnect != tc.wantCan || result.Pending != tc.wantPending || result.Evidence == "" ||
				result.Sender.Name != "Example Sender" || result.Sender.ProfileURN != testMailbox || result.Recipient.ProfileURN != "urn:li:fsd_profile:ACoExamplePerson" {
				t.Fatalf("wrong connection result: %+v; %v", result, err)
			}
			if _, err := time.Parse(time.RFC3339, result.CheckedAt); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConnectRejectsUnverifiedControlsBeforePosting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
		ref      profileRef
	}{
		{name: "missing action", document: connectionDocument(`<p>1st</p>`)},
		{name: "different member", document: connectionDocument(strings.ReplaceAll(connectControl, "123456", "654321"))},
		{name: "different recipient label", document: connectionDocument(strings.ReplaceAll(connectControl, "Invite Example Person", "Invite Someone Else"))},
		{name: "different invite destination", document: connectionDocument(strings.ReplaceAll(connectControl, "vanityName=example-person", "vanityName=someone-else"))},
		{name: "foreign host", document: connectionDocument(strings.ReplaceAll(connectControl, `href="/preload/`, `href="https://example.invalid/preload/`))},
		{name: "disabled action", document: connectionDocument(strings.ReplaceAll(connectControl, `aria-disabled="false"`, `aria-disabled="true"`))},
		{name: "hidden action", document: connectionDocument(`<div hidden>` + connectControl + `</div>`)},
		{name: "template action", document: connectionDocument(`<template>` + connectControl + `</template>`)},
		{name: "conflicting controls", document: connectionDocument(connectControl + pendingControl)},
		{name: "sidebar alone", document: connectionDocument("") + `<aside>` + connectControl + `</aside>`},
		{name: "wrong profile title", document: strings.ReplaceAll(connectionDocument(connectControl), "Example Person | LinkedIn", "Someone Else | LinkedIn")},
		{name: "resolved different URN", ref: profileRef{ID: "ACoDifferentPerson"}, document: connectionDocument(connectControl)},
		{name: "menu alternatives without a current state", document: connectionDocument(connectionMenuScript(t, strings.Replace(connectionMenuFlight, `"modelStates"`, `"unusedStates"`, 1)))},
		{name: "menu state from another member", document: connectionDocument(connectionMenuScript(t, strings.Replace(connectionMenuFlight, "state:invitation:urn:li:member:123456", "state:invitation:urn:li:member:999999", 1)))},
		{name: "unsupported current menu state", document: connectionDocument(connectionMenuScript(t, strings.Replace(connectionMenuFlight, `"stringValue":"Connect"`, `"stringValue":"Withdrawn"`, 1)))},
		{name: "conflicting current menu states", document: connectionDocument(connectionMenuScript(t, strings.Replace(connectionMenuFlight, `"modelStates":[`, `"modelStates":[{"key":{"key":{"value":{"id":"state:invitation:urn:li:member:123456"}}},"value":{"$case":"stringValue","stringValue":"Pending"}},`, 1)))},
		{name: "disabled menu action", document: connectionDocument(connectionMenuScript(t, strings.Replace(connectionMenuFlight, `"modelStates":[`, `"modelStates":[{"key":{"key":{"value":{"id":"connect-button-disabled-example-person"}}},"value":{"$case":"booleanValue","booleanValue":true}},`, 1)))},
		{name: "menu routes to another person", document: connectionDocument(connectionMenuScript(t, strings.ReplaceAll(connectionMenuFlight, "vanityName=example-person", "vanityName=someone-else")))},
		{name: "menu selector uses another member", document: connectionDocument(connectionMenuScript(t, strings.Replace(connectionMenuFlight, `"selectedStateKey":{"key":{"value":{"id":"state:invitation:urn:li:member:123456"`, `"selectedStateKey":{"key":{"value":{"id":"state:invitation:urn:li:member:999999"`, 1)))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := connectionTestClient(t, func() (*http.Response, error) {
				return jsonResponse([]byte(tc.document)), nil
			}, func(r *http.Request) (*http.Response, error) {
				t.Fatalf("unverified target triggered a mutation: %s", r.URL)
				return nil, errors.New("unexpected POST")
			})
			ref := tc.ref
			if ref.ID == "" {
				ref.Slug = "example-person"
			}
			result, err := client.connect(context.Background(), ref, true)
			if err == nil || result.Sent {
				t.Fatalf("unverified target accepted: %+v; %v", result, err)
			}
		})
	}
}

func connectionTestClient(t *testing.T, document func() (*http.Response, error), post roundTripFunc) *linkedinClient {
	t.Helper()
	client := &linkedinClient{session: session{LIAT: "test-token", JSessionID: "ajax:test"}}
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		defer func() { client.lastRequestAt = time.Time{} }()
		if r.URL.Host != "www.linkedin.com" || r.Header.Get("Csrf-Token") != "ajax:test" {
			t.Fatalf("wrong host or missing CSRF token: %s", r.URL)
		}
		if r.Method == http.MethodPost {
			return post(r)
		}
		switch r.URL.Path {
		case "/voyager/api/me":
			return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoSelf"}}`)), nil
		case "/voyager/api/identity/dash/profiles":
			if r.URL.Query().Get("memberIdentity") == "ACoSelf" {
				return jsonResponse([]byte(`{"data":{"*elements":["urn:li:fsd_profile:ACoSelf"]},"included":[{"entityUrn":"urn:li:fsd_profile:ACoSelf","objectUrn":"urn:li:member:654321","firstName":"Example","lastName":"Sender","publicIdentifier":"example-sender"}]}`)), nil
			}
			return jsonResponse(fixture(t, "profile")), nil
		case "/in/example-person/":
			return document()
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
			return nil, errors.New("unexpected request")
		}
	})}
	return client
}
