package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserHeaderSnapshotRejectsSecretsAndInjection(t *testing.T) {
	for _, test := range []struct {
		name      string
		headers   map[string]string
		wantError bool
	}{
		{"captured", map[string]string{"User-Agent": "Captured Chrome", "Sec-CH-UA": `"Chromium";v="157"`, "Referer": "https://hh.ru/vacancy/135582813"}, false},
		{"cookie", map[string]string{"User-Agent": "Captured Chrome", "Cookie": "fixture-private-session"}, true},
		{"authorization", map[string]string{"User-Agent": "Captured Chrome", "Authorization": "fixture-private-secret"}, true},
		{"csrf", map[string]string{"User-Agent": "Captured Chrome", "X-Xsrftoken": "fixture-private-token"}, true},
		{"newline", map[string]string{"User-Agent": "Captured Chrome\r\nX-Injected: fixture-private-secret"}, true},
		{"referer-credentials", map[string]string{"User-Agent": "Captured Chrome", "Referer": "https://fixture-private-secret@hh.ru/vacancy/1"}, true},
		{"foreign-referer", map[string]string{"User-Agent": "Captured Chrome", "Referer": "https://example.com/vacancy/1"}, true},
		{"missing-user-agent", map[string]string{"DNT": "1"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "headers.json")
			data, err := json.Marshal(test.headers)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			headers, err := BrowserHeaders(path)
			if (err != nil) != test.wantError {
				t.Fatalf("snapshot admission: headers=%v err=%v", headers, err)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("header validation exposed its rejected value")
			}
			if err == nil && headers.Get("User-Agent") != "Captured Chrome" {
				t.Error("captured user agent changed")
			}
		})
	}
}

func TestChromeFixturesContainOnlySyntheticApplicationData(t *testing.T) {
	paths, err := filepath.Glob("testdata/chrome/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("missing Chrome fixtures: %v", err)
	}
	approved := map[string]string{
		"_xsrf": "fixture-token", "vacancy_id": "135582813", "resume_hash": strings.Repeat("b", 38),
		"letter":           "Здравствуйте!\r\n\r\nПример работы: https://example.com/work?a=1&b=2",
		"ignore_postponed": "true", "incomplete": "false", "mark_applicant_visible_in_vacancy_country": "false",
		"country_ids": "[]", "lux": "true", "withoutTest": "no", "hhtmFromLabel": "suitable_vacancies", "hhtmSourceLabel": "",
		"isBot": "false", "captchaKey": "00000000-0000-4000-8000-000000000000", "captchaText": "fixture-human-answer", "captchaState": "fixture-captcha-state",
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var exchange chromeExchange
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&exchange); err != nil {
				t.Fatal("unreviewed fixture envelope; inspect privately before importing")
			}
			for key, value := range exchange.Input {
				if expected, ok := approved[key]; !ok || value != expected {
					t.Errorf("field %q is not synthetic; value omitted", key)
				}
			}
			metadata := make(map[string]string)
			for key, value := range exchange.Request.Headers {
				switch strings.ToLower(key) {
				case "x-xsrftoken":
					if value != "fixture-token" {
						t.Error("fixture contains a non-synthetic CSRF value")
					}
				case "origin":
					if value != "https://hh.ru" {
						t.Error("fixture contains an unreviewed origin")
					}
				case "content-type", "accept":
				default:
					metadata[key] = value
				}
			}
			if metadata["referer"] != "https://hh.ru/vacancy/135582813?hhtmFromLabel=suitable_vacancies&hhtmFrom=vacancy" {
				t.Error("fixture Referer is not the synthetic role URL")
			}
			allowed := map[string]bool{"sec-ch-ua-platform": true, "sec-ch-ua": true, "sec-ch-ua-mobile": true, "x-hhtmfrom": true, "x-requested-with": true, "referer": true, "x-hhtmsource": true, "user-agent": true, "dnt": true, "accept-language": true, "priority": true, "sec-fetch-dest": true, "sec-fetch-mode": true, "sec-fetch-site": true}
			for key := range metadata {
				if !allowed[strings.ToLower(key)] {
					t.Errorf("unreviewed fixture header %q; value omitted", key)
				}
			}
			if metadata["accept-language"] != "en-US,en;q=0.9" {
				t.Error("fixture must use a synthetic language preference")
			}
			var response map[string]any
			if err := json.Unmarshal(exchange.Response.Body, &response); err != nil {
				t.Fatal(err)
			}
			assertSyntheticReceipt(t, response)
		})
	}
}

func assertSyntheticReceipt(t *testing.T, values map[string]any) {
	t.Helper()
	for key, value := range values {
		switch key {
		case "hhcaptcha":
			object, ok := value.(map[string]any)
			if !ok {
				t.Error("invalid CAPTCHA fixture structure")
			} else {
				assertSyntheticReceipt(t, object)
			}
		case "captchaState":
			if value != "fixture-captcha-state" {
				t.Error("CAPTCHA fixture contains a real challenge state")
			}
		case "topic_id", "chat_id", "vacancy_id":
			expected := map[string]string{"topic_id": "98765", "chat_id": "54321", "vacancy_id": "135582813"}
			if value != expected[key] {
				t.Errorf("receipt %q is not synthetic", key)
			}
		case "isBot", "captchaError":
			if _, ok := value.(bool); !ok {
				t.Errorf("receipt %q must be a boolean", key)
			}
		case "success":
			if value != "true" {
				t.Error("unreviewed success fixture value")
			}
		default:
			t.Errorf("unreviewed receipt field %q; inspect privately", key)
		}
	}
}
