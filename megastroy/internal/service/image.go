package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxImageBytes = 16 << 20

type ImageResult struct {
	SourceURL    string    `json:"source_url"`
	Path         string    `json:"path"`
	ContentType  string    `json:"content_type"`
	Bytes        int       `json:"bytes"`
	FetchedAt    time.Time `json:"fetched_at"`
	HTTPRequests int       `json:"http_requests"`
}

// Image downloads a URL already returned in a product's images array. It never
// fetches the catalog page again and does not forward catalog cookies to the CDN.
func (c *Client) Image(ctx context.Context, rawURL, output string) (*ImageResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host != "img.megastroycdn.ru" || u.User != nil || !strings.Contains(u.Path, "/products/") {
		return nil, fmt.Errorf("invalid image URL: use an https://img.megastroycdn.ru product URL from images in search or details")
	}
	if output != "" {
		output, err = filepath.Abs(output)
		if err != nil {
			return nil, fmt.Errorf("resolve output %q: %w", output, err)
		}
		if _, err := os.Lstat(output); !os.IsNotExist(err) {
			return nil, fmt.Errorf("output %q must be a new file", output)
		}
		info, err := os.Stat(filepath.Dir(output))
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("output directory %q must already exist", filepath.Dir(output))
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create image request: %w", err)
	}
	req.Header.Set("Accept", "image/jpeg,image/png,image/webp,image/gif")
	response, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image returned HTTP %d; no redirects or retries", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if len(body) > maxImageBytes {
		return nil, fmt.Errorf("image exceeds %d bytes", maxImageBytes)
	}
	contentType := http.DetectContentType(body)
	extension := ""
	switch contentType {
	case "image/jpeg":
		extension = ".jpg"
	case "image/png":
		extension = ".png"
	case "image/webp":
		extension = ".webp"
	case "image/gif":
		extension = ".gif"
	default:
		return nil, fmt.Errorf("expected a product image, received %s", contentType)
	}
	var file *os.File
	if output == "" {
		file, err = os.CreateTemp("", "megastroy-image-*"+extension)
	} else {
		file, err = os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		return nil, fmt.Errorf("create image file: %w", err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(file.Name())
		}
	}()
	if _, err := file.Write(body); err != nil {
		return nil, fmt.Errorf("write image: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close image: %w", err)
	}
	path, err := filepath.Abs(file.Name())
	if err != nil {
		return nil, fmt.Errorf("resolve saved image path: %w", err)
	}
	complete = true
	return &ImageResult{SourceURL: u.String(), Path: path, ContentType: contentType, Bytes: len(body), FetchedAt: time.Now().UTC(), HTTPRequests: c.requests}, nil
}
