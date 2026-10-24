package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }

func TestInvalidInputDoesNotReadCookies(t *testing.T) {
	t.Parallel()

	// An attempted auth read would fail on this empty path instead of returning
	// the input or cancellation error asserted below.
	client, clientErr := NewClient(NewHTTPClient(), AuthStore{})
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	u, parseErr := url.Parse(WebOrigin + "/web/1/js/items")
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if _, err := client.Prepare(t.Context(), u, RequestOptions{UserAgent: "bad\r\nheader"}, false); err == nil || !strings.Contains(err.Error(), "user-agent") {
		t.Fatal("accepted invalid UA")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Prepare(cancelled, u, RequestOptions{}, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func Test429ReportsRetryAfterAndNeverRetries(t *testing.T) {
	t.Parallel()

	for _, retry := range []string{"", "90", "Mon, 07 Sep 2026 20:00:00 GMT"} {
		t.Run(retry, func(t *testing.T) {
			t.Parallel()

			requests := 0
			httpClient := NewHTTPClient()
			httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {retry}}, Body: io.NopCloser(strings.NewReader(`{"too-many-requests":{"message":"IP restricted"}}`)), Request: req}, nil
			})
			client, clientErr := NewClient(httpClient, testAuthStore(t))
			if clientErr != nil {
				t.Fatal(clientErr)
			}
			for range 2 {
				_, err := client.Search(t.Context(), webOptions(), RequestOptions{})
				var failure *HTTPError
				if !errors.As(err, &failure) || failure.Status != 429 || failure.RetryAfter != retry {
					t.Fatalf("wrong error: %v", err)
				}
				if !strings.Contains(err.Error(), "HTTP 429") || (retry != "" && !strings.Contains(err.Error(), "Retry-After: "+retry)) {
					t.Fatal(err)
				}
			}
			if requests != 2 {
				t.Fatalf("got %d requests for two calls; unexpected retry or suppression", requests)
			}
		})
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	client, clientErr := NewClient(NewHTTPClient(), AuthStore{})
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	req, requestErr := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	_, err := client.Do(&PreparedRequest{request: req})
	var failure *HTTPError
	if !errors.As(err, &failure) || failure.Status != 302 || requests.Load() != 1 {
		t.Fatalf("redirect behavior: requests=%d, error=%v", requests.Load(), err)
	}
}

func Test429PreservesStatusWhenBodyReadFails(t *testing.T) {
	t.Parallel()

	httpClient := NewHTTPClient()
	requests := 0
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"15"}}, Body: brokenBody{}, Request: req}, nil
	})
	client, clientErr := NewClient(httpClient, testAuthStore(t))
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	_, err := client.Search(t.Context(), webOptions(), RequestOptions{})
	var failure *HTTPError
	if !errors.As(err, &failure) || failure.Status != 429 || failure.RetryAfter != "15" || requests != 1 {
		t.Fatalf("lost status or retry hint: %v; requests=%d", err, requests)
	}
}
