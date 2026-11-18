package service_test

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"github.com/meoyawn/clis/habr-career/internal/service"
)

func TestAboutUpdatesOnlyTheOwnedBiographyAndChecksEmployerView(t *testing.T) {
	for _, missingPrint := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-print-%t", missingPrint), func(t *testing.T) {
			const alias = "synthetic-user"
			const newText = "<p>Authors publish once.</p>"
			current := "<p>Old text</p>"
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/frontend_v1/users/me":
					_ = json.NewEncoder(w).Encode(service.Object{"user": service.Object{"alias": alias}})
				case "/profile/personal/edit":
					writePage(w, service.Object{"selectedCitizenships": []any{service.Object{"value": 3}, service.Object{"value": 7}}})
					fmt.Fprintf(w, `<a id="top_avatar" href="/%s">Owner</a><form action="/profile/personal" method="post" enctype="multipart/form-data"><input name="_method" value="patch"><input name="authenticity_token" value="synthetic"><input name="user[login]" value="%s"><input name="user[address]" value="original"><input type="file" name="user[avatar]"><textarea name="user[about]">%s</textarea></form>`, alias, alias, html.EscapeString(current))
				case "/profile/personal":
					writes++
					if err := r.ParseMultipartForm(1 << 16); err != nil {
						t.Error(err)
						http.Error(w, "bad form", 400)
						return
					}
					if r.Method != http.MethodPost || r.FormValue("_method") != "patch" || r.FormValue("authenticity_token") != "synthetic" || r.FormValue("user[login]") != alias || r.FormValue("user[address]") != "original" || !slices.Equal(r.MultipartForm.Value["user[citizenship_country_ids][]"], []string{"3", "7"}) || r.FormValue("user[about]") != newText {
						t.Error("personal form lost metadata, changed text or used the wrong owner")
					}
					current = r.FormValue("user[about]")
					w.Header().Set("Location", "/"+alias)
					w.WriteHeader(http.StatusSeeOther)
				case "/" + alias:
					school := service.Object{"title": "Synthetic University", "users": service.Object{"href": "/resumes?university_ids=1", "title": fmt.Sprintf("%d alumni", 123+writes)}}
					writePage(w, service.Object{"user": service.Object{"id": alias, "title": "Synthetic", "isOwner": true, "shouldDisplayInfo": true, "print": service.Object{"html": service.Origin + "/" + alias + "/print"}}, "resume": service.Object{"about": service.Object{"value": current}, "education": service.Object{"items": []any{school}}}})
				case "/" + alias + "/print":
					printed := current
					if missingPrint {
						printed = "<p>Old text</p>"
					}
					fmt.Fprintf(w, `<div class="content"><h1 class="full_name">Synthetic</h1><a href="%s/%s">Profile</a>%s</div>`, service.Origin, alias, printed)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			httpClient := server.Client()
			httpClient.Transport = routeTransport{target: target, transport: httpClient.Transport}
			id, _ := domain.NewResumeIDFromValue(alias)
			result, err := service.NewClient(httpClient).UpdateAbout(t.Context(), id, newText, true, t.TempDir())
			if writes != 1 || missingPrint != (err != nil) || !missingPrint && result["verified"] != true {
				t.Fatalf("writes=%d result=%v err=%v", writes, result, err)
			}
		})
	}
}
