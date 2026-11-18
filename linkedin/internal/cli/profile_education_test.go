package cli

import "testing"

func TestEducationCardsBindCaptionsAndExternalURLsToOneSchool(t *testing.T) {
	const page = `0:[{"viewTrackingSpecs":{"viewName":"education-lockup-view"},"children":[{"requestedArguments":{"payload":{"educationId":"11"}}},{"viewTrackingSpecs":{"viewName":"education-treasury-thumbnail"},"children":[{"urlValue":{"url":"https://elsewhere.example/"}},{"children":["Demo"]}]}]},{"viewTrackingSpecs":{"viewName":"education-lockup-view"},"children":[{"requestedArguments":{"payload":{"educationId":"42"}}},"$Lb","$Lc"]}]
b:{"viewTrackingSpecs":{"viewName":"education-treasury-thumbnail"},"children":[{"urlValue":{"url":"https://a.example/"}},{"children":["Demo"]}]}
c:{"viewTrackingSpecs":{"viewName":"education-treasury-thumbnail"},"children":[{"urlValue":{"url":"https://b.example/"}},{"children":["Client"]}]}
`
	cards := []educationCard{{ID: "1", Title: "Demo"}, {ID: "2", Title: "Client"}}
	if err := bindEducationLinks([]byte(page), "42", cards); err != nil {
		t.Fatal(err)
	}
	if cards[0].URL != "https://a.example/" || cards[1].URL != "https://b.example/" {
		t.Fatalf("wrong destinations: %+v", cards)
	}
	if err := bindEducationLinks([]byte(page), "11", cards); err == nil {
		t.Fatal("accepted cards from another school")
	}
	if err := bindEducationLinks([]byte(page), "42", cards[:1]); err == nil {
		t.Fatal("ignored an unexpected card")
	}
}
