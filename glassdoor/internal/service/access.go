package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

const VerificationURL = Origin + "/Salaries/index.htm"

// HumanVerificationError requires a person to complete the website's challenge.
// It is distinct from an ordinary forbidden response or a rate limit.
type HumanVerificationError struct {
	StatusCode int
}

func (e *HumanVerificationError) Error() string {
	return fmt.Sprintf("glassdoor human verification required (HTTP %d); stopped without retry", e.StatusCode)
}

func readResponse(res *http.Response) ([]byte, error) {
	if res.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("HTTP 429, Retry-After=%q; stopped without retry", res.Header.Get("Retry-After"))
	}
	if strings.EqualFold(res.Header.Get("Cf-Mitigated"), "challenge") {
		return nil, &HumanVerificationError{StatusCode: res.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read Glassdoor response: %w", err)
	}
	if !strings.Contains(res.Header.Get("Content-Type"), "application/json") {
		page := strings.ToLower(string(data))
		if strings.Contains(page, "<title>just a moment") || strings.Contains(page, "humans only") {
			return nil, &HumanVerificationError{StatusCode: res.StatusCode}
		}
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d, Retry-After=%q; stopped without retry", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("glassdoor response exceeds 8 MiB; stopped")
	}
	return data, nil
}
