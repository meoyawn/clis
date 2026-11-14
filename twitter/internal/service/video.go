package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Video struct {
	Path   string `json:"path"`
	MIME   string `json:"mime_type"`
	Size   int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	data   []byte
}

func LoadVideo(path string) (*Video, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open video %q: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 12 || info.Size() > 512<<20 {
		return nil, fmt.Errorf("video %q must be a regular MP4 file of 12–536870912 bytes", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, (512<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 512<<20 || http.DetectContentType(data) != "video/mp4" {
		return nil, fmt.Errorf("video %q must be MP4 and at most 512 MiB", path)
	}
	return &Video{Path: path, MIME: "video/mp4", Size: len(data), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), data: data}, nil
}

type videoUploadStatus struct {
	ID         string `json:"media_id_string"`
	Processing *struct {
		State      string `json:"state"`
		CheckAfter int    `json:"check_after_secs"`
		Error      *struct {
			Code int    `json:"code"`
			Name string `json:"name"`
		} `json:"error"`
	} `json:"processing_info"`
}

func (c *Client) uploadVideo(ctx context.Context, video *Video) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Media transfers can exceed the ordinary API's 30-second deadline. Keep
	// that longer timeout local to uploads and retain single-attempt requests.
	mediaHTTP := *c.http
	mediaHTTP.Timeout = 2 * time.Minute
	uploader := *c
	uploader.http = &mediaHTTP
	c = &uploader
	const endpoint = "https://upload.x.com/i/media/upload.json"
	q := url.Values{"command": {"INIT"}, "total_bytes": {strconv.Itoa(video.Size)}, "media_type": {video.MIME}, "media_category": {"tweet_video"}}
	data, err := c.request(ctx, http.MethodPost, endpoint+"?"+q.Encode(), "", nil)
	if err != nil {
		return "", fmt.Errorf("video INIT failed before post creation: %w", err)
	}
	var status videoUploadStatus
	if json.Unmarshal(data, &status) != nil || status.ID == "" {
		return "", fmt.Errorf("video INIT returned no media ID")
	}
	id := status.ID
	const chunkSize = 1 << 20
	for offset, segment := 0, 0; offset < len(video.data); offset, segment = offset+chunkSize, segment+1 {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("media", "blob")
		if err != nil {
			return "", err
		}
		if _, err = part.Write(video.data[offset:min(offset+chunkSize, len(video.data))]); err != nil {
			return "", err
		}
		if err = writer.Close(); err != nil {
			return "", err
		}
		q = url.Values{"command": {"APPEND"}, "media_id": {id}, "segment_index": {strconv.Itoa(segment)}}
		if _, err = c.request(ctx, http.MethodPost, endpoint+"?"+q.Encode(), writer.FormDataContentType(), body.Bytes()); err != nil {
			return "", fmt.Errorf("video %q APPEND segment %d failed before post creation: %w", id, segment, err)
		}
	}
	q = url.Values{"command": {"FINALIZE"}, "media_id": {id}}
	data, err = c.request(ctx, http.MethodPost, endpoint+"?"+q.Encode(), "", nil)
	if err != nil {
		return "", fmt.Errorf("video %q FINALIZE failed before post creation: %w", id, err)
	}
	for {
		status = videoUploadStatus{}
		if json.Unmarshal(data, &status) != nil || status.ID != id {
			return "", fmt.Errorf("video upload status does not match media %q", id)
		}
		if status.Processing == nil {
			return id, nil
		}
		switch status.Processing.State {
		case "succeeded":
			return id, nil
		case "failed":
			return "", fmt.Errorf("video %q processing failed: %+v", id, status.Processing.Error)
		case "pending", "in_progress":
		default:
			return "", fmt.Errorf("video %q has unsupported processing state %q", id, status.Processing.State)
		}
		delay := status.Processing.CheckAfter
		if delay < 0 || delay > 60 {
			return "", fmt.Errorf("video %q requested unsupported polling delay %d", id, delay)
		}
		timer := time.NewTimer(time.Duration(delay) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
		q = url.Values{"command": {"STATUS"}, "media_id": {id}}
		data, err = c.request(ctx, http.MethodGet, endpoint+"?"+q.Encode(), "", nil)
		if err != nil {
			return "", fmt.Errorf("video %q STATUS failed before post creation: %w", id, err)
		}
	}
}
