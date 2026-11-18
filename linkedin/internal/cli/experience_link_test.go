package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

// Synthetic values follow the native Add a link and uploadPositionMedia
// contract: old IDs are removed, new ingestion and title bindings are carried
// into the request, and the sort expression comes from the edited collection.
func TestExperienceLinkSerializesOneReplacementAndPreservesPositionFields(t *testing.T) {
	for _, scenario := range []string{"replace", "preview-failed", "inconsistent-ingestion"} {
		t.Run(scenario, func(t *testing.T) {
			const current = `{"modelStates":[
{"key":{"key":{"value":{"id":"description"}}},"value":{"$case":"stringValue","stringValue":"Existing product work"}},
{"key":{"key":{"value":{"id":"notify"}}},"value":{"$case":"booleanValue","booleanValue":false}},
{"key":{"key":{"value":{"id":"skills"}}},"value":{"$case":"stringListValue","stringListValue":{"stringValues":["Go","SQLite"]}}},
{"key":{"key":{"value":{"id":"existing"}}},"value":{"$case":"stringListValue","stringListValue":{"stringValues":["10","20","30"]}}},
{"key":{"key":{"value":{"id":"sorted"}}},"value":{"$case":"expression","expression":{"expression":{"$case":"stringListExpression","stringListExpression":{"expression":{"$case":"collectionIdsStringList","collectionIdsStringList":{"collectionId":"position-media"}}}}}}}
],"collection":{"collectionId":"position-media","initialItems":[{"key":"10"},{"key":"20"},{"key":"30"}]},
"save":{"$type":"proto.sdui.actions.core.ServerRequest","value":{"requestId":"com.linkedin.sdui.impl.profile.components.forms.uploadPositionMedia","requestedArguments":{
"requestedStateKeys":[{"key":{"value":{"id":"description"}}},{"key":{"value":{"id":"notify"}}},{"key":{"value":{"id":"skills"}}},{"key":{"value":{"id":"existing"}}},{"key":{"value":{"id":"ingested"}}},{"key":{"value":{"id":"bindings"}}},{"key":{"value":{"id":"sorted"}}}],
"payload":{"description":{"key":"description","namespace":"MemoryNamespace"},"allowProfileEditBroadcasts":{"key":"notify","namespace":"MemoryNamespace"},"allMediaIds":{"key":"existing","namespace":"MemoryNamespace"},"ingestedContentIds":{"key":"ingested","namespace":"MemoryNamespace"},"ingestedMediaIdToBindingIdMap":{"key":"bindings","namespace":"MemoryNamespace"},"sortedMediaIds":{"key":"sorted","namespace":"MemoryNamespace"}}}}}}`
			var row any
			if err := json.Unmarshal([]byte(current), &row); err != nil {
				t.Fatal(err)
			}
			form, err := profileFormData(documentData{"0": row})
			if err != nil {
				t.Fatal(err)
			}
			action, err := profileFormAction(form.data, uploadPositionMedia)
			if err != nil {
				t.Fatal(err)
			}
			preview := `0:{"modelStates":[{"key":{"key":{"value":{"id":"new-card-mediaTitle"}}},"value":{"$case":"stringValue","stringValue":"Example product"}}],"actions":[{"$type":"proto.sdui.actions.core.AddToStringList","value":{"newString":"99","key":{"key":{"value":{"id":"ingested"}}}}},{"$type":"proto.sdui.actions.core.AddToStringList","value":{"newString":"99:new-card","key":{"key":{"value":{"id":"bindings"}}}}}]}`
			if scenario == "preview-failed" {
				preview = `0:{"modelStates":[{"key":{"key":{"value":{"id":"error"}}},"value":{"$case":"stringValue","stringValue":"No preview"}}]}`
			}
			if scenario == "inconsistent-ingestion" {
				preview = `0:{"modelStates":[{"key":{"key":{"value":{"id":"new-card-mediaTitle"}}},"value":{"$case":"stringValue","stringValue":"Example product"}}],"actions":[{"$type":"proto.sdui.actions.core.AddToStringList","value":{"newString":"99","key":{"key":{"value":{"id":"ingested"}}}}},{"$type":"proto.sdui.actions.core.AddToStringList","value":{"newString":"88:new-card","key":{"key":{"value":{"id":"bindings"}}}}}]}`
			}
			title, err := prepareExperienceLink(form, action, []byte(preview), []any{}, "")
			if scenario != "replace" {
				if err == nil {
					t.Fatal("unconfirmed preview was accepted for a profile write")
				}
				return
			}
			if err != nil || title != "Example product" {
				t.Fatalf("replacement not prepared: title=%q err=%v", title, err)
			}
			calls := 0
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Query().Get("sduiid") != uploadPositionMedia {
					return nil, fmt.Errorf("unexpected media request: %s %s", r.Method, r.URL)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					return nil, err
				}
				values := map[string]any{}
				for _, item := range body["states"].([]any) {
					state := item.(map[string]any)
					values[state["key"].(string)] = state["value"]
				}
				want := map[string]any{"description": "Existing product work", "notify": false, "skills": []any{"Go", "SQLite"}, "existing": []any{}, "ingested": []any{"99"}, "bindings": []any{"99:new-card"}, "sorted": []any{"new-card"}}
				if !reflect.DeepEqual(values, want) {
					t.Fatalf("media replacement retained old cards or changed position content: %v", values)
				}
				return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[{}]}}}`)), nil
			})}}
			if _, err := client.sendProfileAction(context.Background(), form, action, "com.linkedin.sdui.flagshipnav.profile.ProfilePositionForm"); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("expected one native request, got %d", calls)
			}
		})
	}
}

func TestExperienceLinkReadbackRequiresOneDirectDestinationOnTheExactPosition(t *testing.T) {
	const destination = "https://example.org/"
	for _, tc := range []struct {
		name, position string
		links          []profileLink
		valid          bool
	}{
		{"direct", "42", []profileLink{{URL: destination}}, true},
		{"https-upgrade", "42", []profileLink{{URL: destination}}, true},
		{"foreign-https-upgrade", "42", []profileLink{{URL: "https://other.example/"}}, false},
		{"media-overlay", "42", []profileLink{{URL: "https://www.linkedin.com/in/example-person/overlay/Position/42/treasury/"}}, false},
		{"extra-card", "42", []profileLink{{URL: destination}, {URL: "https://other.example/"}}, false},
		{"wrong-position", "77", []profileLink{{URL: destination}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := fullProfileResult{Sections: []profileSection{{Title: "Experience", Blocks: []profileBlock{
				{Links: []profileLink{{URL: "https://www.linkedin.com/in/example-person/details/experience/edit/forms/" + tc.position + "/"}}},
				{Links: tc.links},
			}}}}
			expected := destination
			if tc.name == "https-upgrade" || tc.name == "foreign-https-upgrade" {
				expected = "http://example.org/"
			}
			links, _ := positionMediaLinks(profile, "42")
			if err := verifyPositionCards(links, "42", nil, expected, ""); (err == nil) != tc.valid {
				t.Fatalf("direct link verification valid=%v, got %v", tc.valid, err)
			}
		})
	}
}
