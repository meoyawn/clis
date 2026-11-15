package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// BrowserHeaders imports non-secret headers captured from the selected Chrome
// session. Cookies and CSRF stay in the live jar, never in this snapshot.
func BrowserHeaders(path string) (http.Header, error) {
	headers := make(http.Header)
	if path == "" {
		return headers, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read browser headers file: %w", err)
	}
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("decode browser headers file: %w", err)
	}
	for name, value := range values {
		if _, exists := headers[http.CanonicalHeaderKey(name)]; exists {
			return nil, fmt.Errorf("browser header %q is duplicated with different casing", name)
		}
		switch strings.ToLower(name) {
		case "user-agent", "accept-language", "dnt", "sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "sec-ch-ua-arch", "sec-ch-ua-bitness", "sec-ch-ua-full-version", "sec-ch-ua-full-version-list", "sec-ch-ua-model", "sec-ch-ua-platform-version", "referer", "x-hhtmfrom", "x-hhtmsource", "sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "priority", "baggage", "sentry-trace":
			if strings.ContainsAny(value, "\r\n") {
				return nil, fmt.Errorf("browser header %q contains a newline", name)
			}
			headers.Set(name, value)
		default:
			return nil, fmt.Errorf("browser header %q is not allowed; cookies and tokens must come from the selected session", name)
		}
	}
	if headers.Get("User-Agent") == "" {
		return nil, fmt.Errorf("browser headers file requires User-Agent from the selected Chrome session")
	}
	if referer := headers.Get("Referer"); referer != "" {
		page, err := url.Parse(referer)
		if err != nil || page.Scheme != "https" || !IsHHHost(page.Hostname()) || page.User != nil || page.Port() != "" || page.Fragment != "" {
			return nil, fmt.Errorf("browser Referer must be an HTTPS hh.ru page without credentials, port or fragment")
		}
	}
	return headers, nil
}

func installedChromeUserAgent(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("automatic Chrome version detection requires macOS; provide --headers-file from the selected Chrome session")
	}
	data, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "/Applications/Google Chrome.app/Contents/Info", "CFBundleShortVersionString").Output()
	if err != nil {
		return "", fmt.Errorf("read installed Chrome version: %w; provide --headers-file", err)
	}
	major, _, _ := strings.Cut(strings.TrimSpace(string(data)), ".")
	if major == "" || strings.Trim(major, "0123456789") != "" {
		return "", fmt.Errorf("installed Chrome version has no numeric major; provide --headers-file")
	}
	return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + major + ".0.0.0 Safari/537.36", nil
}
