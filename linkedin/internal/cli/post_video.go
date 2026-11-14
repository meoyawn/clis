package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/service"
)

type videoUploadPart struct {
	URL       string            `json:"uploadUrl"`
	Headers   map[string]string `json:"headers"`
	FirstByte int               `json:"firstByte"`
	LastByte  int               `json:"lastByte"`
}

type videoRegistration struct {
	URN              string            `json:"urn"`
	Type             string            `json:"type"`
	Recipes          []string          `json:"recipes"`
	MediaArtifactURN string            `json:"mediaArtifactUrn"`
	Metadata         json.RawMessage   `json:"multipartMetadata"`
	Parts            []videoUploadPart `json:"partUploadRequests"`
	SingleURL        string            `json:"singleUploadUrl"`
	SingleHeaders    map[string]string `json:"singleUploadHeaders"`
}

type preparedVideo struct {
	SHA256  string   `json:"sha256"`
	Author  string   `json:"author"`
	URN     string   `json:"urn"`
	Recipes []string `json:"recipes"`
	Ready   bool     `json:"ready"`
}

func (c *linkedinClient) videoJSON(ctx context.Context, path string, body []byte, receipt string) ([]byte, error) {
	resp, err := c.request(ctx, http.MethodPost, path, bytes.NewReader(body), http.Header{"Accept": {"application/json"}, "Content-Type": {"application/json"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("video response incomplete: %w", err)
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("video response exceeds 4 MiB")
	}
	if err := os.WriteFile(receipt, data, 0600); err != nil {
		return nil, err
	}
	return data, nil
}

func validateVideoParts(reg videoRegistration, size int) ([]videoUploadPart, error) {
	parts := reg.Parts
	if reg.Type == "SINGLE" {
		parts = []videoUploadPart{{URL: reg.SingleURL, Headers: reg.SingleHeaders, FirstByte: 0, LastByte: size - 1}}
	} else if reg.Type != "MULTIPART" || reg.MediaArtifactURN == "" || len(reg.Metadata) == 0 {
		return nil, fmt.Errorf("unsupported video upload mechanism %q or missing multipart metadata", reg.Type)
	}
	next := 0
	for _, part := range parts {
		u, err := url.Parse(part.URL)
		if err != nil || u.Scheme != "https" || u.Host != "www.linkedin.com" || u.User != nil || !strings.HasPrefix(u.Path, "/dms-uploads/") {
			return nil, fmt.Errorf("video upload destination is outside LinkedIn's dms-uploads endpoint")
		}
		if part.FirstByte != next || part.LastByte < next || part.LastByte >= size {
			return nil, fmt.Errorf("video upload ranges are incomplete, overlapping or exceed %d bytes", size)
		}
		next = part.LastByte + 1
	}
	if next != size {
		return nil, fmt.Errorf("video upload covers %d of %d bytes", next, size)
	}
	return parts, nil
}

func (c *linkedinClient) preparePostVideo(ctx context.Context, video *service.PostVideo, author, dir string) (preparedVideo, error) {
	result := preparedVideo{SHA256: video.SHA256, Author: author}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	base := filepath.Join(dir, video.SHA256)
	if data, err := os.ReadFile(base + ".ready.json"); err == nil {
		if json.Unmarshal(data, &result) != nil || result.SHA256 != video.SHA256 || result.Author != author || !result.Ready {
			return result, fmt.Errorf("prepared video record %q does not match the file and author", base)
		}
		return result, c.waitPostVideo(ctx, result.URN, base)
	}
	f, err := os.OpenFile(base+".attempt.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, fmt.Errorf("reserve video upload; inspect existing %q before retrying: %w", base, err)
	}
	writeErr := writeJSON(f, map[string]any{"video": video, "author": author, "attempted_at": time.Now().UTC().Format(time.RFC3339Nano)})
	syncErr, closeErr := f.Sync(), f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return result, err
	}
	body := mustJSON(map[string]any{"mediaUploadType": "VIDEO_SHARING", "fileSize": video.Size, "filename": filepath.Base(video.Path)})
	data, err := c.videoJSON(ctx, "/voyager/api/voyagerVideoDashMediaUploadMetadata?action=upload", body, base+".registration.json")
	if err != nil {
		return result, err
	}
	var envelope struct {
		Value videoRegistration `json:"value"`
		Data  struct {
			Value videoRegistration `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return result, err
	}
	reg := envelope.Value
	if reg.URN == "" {
		reg = envelope.Data.Value
	}
	if !strings.HasPrefix(reg.URN, "urn:li:digitalmediaAsset:") || len(reg.Recipes) == 0 {
		return result, fmt.Errorf("video registration has no asset URN or recipes; inspect %q", base)
	}
	parts, err := validateVideoParts(reg, video.Size)
	if err != nil {
		return result, err
	}
	responses := []any{}
	for i, part := range parts {
		u, _ := url.Parse(part.URL)
		headers := http.Header{}
		for key, value := range part.Headers {
			headers.Set(key, value)
		}
		reader, err := video.Reader(part.FirstByte, part.LastByte)
		if err != nil {
			return result, err
		}
		resp, err := c.request(ctx, http.MethodPut, u.RequestURI(), reader, headers)
		if err != nil {
			return result, fmt.Errorf("video part %d upload failed; inspect %q: %w", i, base, err)
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return result, readErr
		}
		partHeaders := map[string]string{}
		for key := range resp.Header {
			partHeaders[strings.ToLower(key)] = resp.Header.Get(key)
		}
		partReceipt := map[string]any{"headers": partHeaders, "httpStatusCode": resp.StatusCode}
		if err := os.WriteFile(fmt.Sprintf("%s.part-%d.json", base, i), mustJSON(partReceipt), 0600); err != nil {
			return result, err
		}
		responses = append(responses, partReceipt)
	}
	if reg.Type == "MULTIPART" {
		body := mustJSON(map[string]any{"completeUploadRequest": map[string]any{"partUploadResponses": responses, "mediaArtifactUrn": reg.MediaArtifactURN, "multipartMetadata": reg.Metadata}})
		if _, err := c.videoJSON(ctx, "/voyager/api/voyagerVideoDashMediaUploadMetadata?action=completeMultipartUpload", body, base+".completed.json"); err != nil {
			return result, err
		}
	}
	result.URN, result.Recipes = reg.URN, reg.Recipes
	if err := c.waitPostVideo(ctx, result.URN, base); err != nil {
		return result, err
	}
	result.Ready = true
	return result, os.WriteFile(base+".ready.json", mustJSON(result), 0600)
}

func (c *linkedinClient) waitPostVideo(ctx context.Context, urn, base string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		var status map[string]any
		if err := c.getJSON(ctx, "/voyager/api/voyagerVideoDashMediaAssetStatus/"+url.PathEscape(urn)+"?mediaStatusType=VIDEO", &status); err != nil {
			return err
		}
		if err := os.WriteFile(base+".status.json", mustJSON(status), 0600); err != nil {
			return err
		}
		if data, ok := status["data"].(map[string]any); ok {
			status = data
		}
		state := jsonStringAt(status, "processingStatus")
		if state == "READY" {
			return nil
		}
		if state != "PROCESSING" {
			return fmt.Errorf("video %q has unsupported or failed processing status %q; inspect %q.status.json", urn, state, base)
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
