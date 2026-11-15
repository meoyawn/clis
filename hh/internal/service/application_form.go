package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// FormDataEncoder::GenerateUniqueBoundaryString in Chromium uses this exact
// prefix, 16 random bytes, and a 64-character map with A/B repeated at the end.
func chromeMultipartBoundary() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789AB"
	var random [16]byte
	_, _ = rand.Read(random[:])
	for i := range random {
		random[i] = alphabet[random[i]&63]
	}
	return "----WebKitFormBoundary" + string(random[:])
}

func (c *Client) postApplicationForm(ctx context.Context, fields Object) ([]byte, error) {
	endpoint := c.base.ResolveReference(&url.URL{Path: "/applicant/vacancy_response/popup"})
	var xsrf string
	if c.http.Jar != nil {
		for _, cookie := range c.http.Jar.Cookies(endpoint) {
			if cookie.Name == "_xsrf" {
				xsrf = cookie.Value
			}
		}
	}
	if xsrf == "" {
		return nil, fmt.Errorf("missing _xsrf cookie for %s; no application attempted", endpoint.Host)
	}
	request := Object{"_xsrf": xsrf}
	for key, value := range fields {
		if key == "_xsrf" && value != xsrf {
			return nil, fmt.Errorf("application _xsrf field differs from the current session; no request attempted")
		}
		request[key] = value
	}
	order := []string{"_xsrf", "isBot", "vacancy_id", "resume_hash", "ignore_postponed", "incomplete", "mark_applicant_visible_in_vacancy_country", "country_ids", "letter", "lux", "withoutTest", "hhtmFromLabel", "hhtmSourceLabel"}
	seen := make(map[string]bool, len(order))
	for _, name := range order {
		seen[name] = true
	}
	var extra []string
	for name := range request {
		if !seen[name] && name != "captchaKey" && name != "captchaText" && name != "captchaState" {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)
	order = append(order, "captchaKey", "captchaText", "captchaState")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(chromeMultipartBoundary()); err != nil {
		return nil, fmt.Errorf("set Chrome multipart boundary: %w", err)
	}
	for _, name := range order {
		value, exists := request[name]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("application field %q must be a string; no request attempted", name)
		}
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
		if err := writer.WriteField(name, strings.ReplaceAll(text, "\n", "\r\n")); err != nil {
			return nil, fmt.Errorf("encode application field %q: %w", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return c.requestBody(ctx, http.MethodPost, endpoint.Path, nil, request, false, &body, writer.FormDataContentType())
}
