package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureImageURL = "https://img.megastroycdn.ru/signature/products/hash/123_1.png"

func TestImageDownloadPreservesBytesWithoutCatalogCookies(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	// A real PNG response, independent of the downloader's MIME and file logic.
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jT1sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	client, requests := servedClient(t, string(png), 200, nil, func(r *http.Request) {
		if r.URL.Path != "/signature/products/hash/123_1.png" || r.Header.Get("Cookie") != "" {
			t.Errorf("incorrect CDN request or leaked catalog cookies: %s", r.URL)
		}
	})
	got, err := client.Image(context.Background(), fixtureImageURL, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got.Path)
	if err != nil || !bytes.Equal(data, png) || !filepath.IsAbs(got.Path) || got.ContentType != "image/png" || got.Bytes != len(png) || got.HTTPRequests != 1 || requests.Load() != 1 || got.SourceURL != fixtureImageURL {
		t.Fatalf("download did not produce the advertised image: %+v %v", got, err)
	}
	if _, err := client.Image(context.Background(), fixtureImageURL, ""); err == nil || requests.Load() != 1 {
		t.Fatal("image client exceeded its single-request budget")
	}
}

func TestImageRejectsErrorPagesAndRedirects(t *testing.T) {
	for _, tt := range []struct {
		status  int
		message string
	}{
		{200, "expected a product image"},
		{302, "HTTP 302"},
		{403, "HTTP 403"},
	} {
		t.Run(tt.message, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "picture.jpg")
			client, requests := servedClient(t, "<html>Not an image</html>", tt.status, http.Header{"Location": {"https://example.com/"}}, nil)
			result, err := client.Image(context.Background(), fixtureImageURL, output)
			if err == nil || !strings.Contains(err.Error(), tt.message) || result != nil || requests.Load() != 1 {
				t.Fatalf("invalid image accepted or retried: result=%v err=%v requests=%d", result, err, requests.Load())
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("failed download left an output file")
			}
		})
	}
}

func TestImageRefusesOverwriteAndForeignURLBeforeRequest(t *testing.T) {
	output := filepath.Join(t.TempDir(), "existing.jpg")
	if err := os.WriteFile(output, []byte("keep this file"), 0600); err != nil {
		t.Fatal(err)
	}
	client, requests := servedClient(t, "", 200, nil, nil)
	if _, err := client.Image(context.Background(), fixtureImageURL, output); err == nil || requests.Load() != 0 {
		t.Fatal("existing output was not rejected before network access")
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "keep this file" {
		t.Fatal("overwrote the user's file")
	}
	if _, err := client.Image(context.Background(), "https://example.com/products/123.jpg", ""); err == nil || requests.Load() != 0 {
		t.Fatal("foreign URL reached the network")
	}
}
