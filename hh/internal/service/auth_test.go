package service

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBrowserCookieWireValuesAndResponseLifecycle(t *testing.T) {
	// Chrome sends its JSON-valued chat filter cookie with literal quotes.
	// Go's default Cookie.String strips those quotes, corrupting the value.
	const filter = `{"unread":false,"textOnly":false}`
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		wire := r.Header.Get("Cookie")
		if !strings.Contains(wire, "filters123="+filter) || !strings.Contains(wire, "auth=fixture-secret") {
			t.Errorf("browser cookie values changed on the wire: %q", wire)
		}
		if reads == 2 && !strings.Contains(wire, "renewed=yes") {
			t.Error("response cookie was not preserved for the next request")
		}
		http.SetCookie(w, &http.Cookie{Name: "renewed", Value: "yes", Path: "/"})
		w.WriteHeader(204)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(base, []*http.Cookie{{Name: "filters123", Value: filter, Path: "/"}, {Name: "auth", Value: "fixture-secret", Path: "/"}})
	client := browserHTTPClient(base, jar, server.Client().Transport)
	for range 2 {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/chat/888", nil)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
}

func TestBrowserCookieRejectsHeaderInjectionWithoutLeakingValue(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	jar, _ := cookiejar.New(nil)
	const unsafe = "secret-value\r\nX-Injected: yes"
	jar.SetCookies(base, []*http.Cookie{{Name: "filters123", Value: unsafe, Path: "/"}})
	client := browserHTTPClient(base, jar, server.Client().Transport)
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/chat/888", nil)
	_, err := client.Do(request)
	if err == nil || requests != 0 || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unsafe cookie reached network or error leaked its value: requests=%d err=%v", requests, err)
	}
}
