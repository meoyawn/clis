package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// An accepted APPEND is not a playable video. The upload must preserve every
// byte across chunk boundaries and wait for the same asset to finish processing.
func TestVideoUploadChunksAndProcessing(t *testing.T) {
	for _, tc := range []struct {
		name, final string
		wantError   bool
	}{
		{"ready", `{"media_id_string":"456","processing_info":{"state":"succeeded"}}`, false},
		{"processing failed", `{"media_id_string":"456","processing_info":{"state":"failed","error":{"code":3,"name":"UnsupportedCodec"}}}`, true},
		{"different asset", `{"media_id_string":"999","processing_info":{"state":"succeeded"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Repeat([]byte{0, 1, 2, 3, 4, 5, 6}, (1<<20)/7+10)
			video := &Video{Size: len(data), MIME: "video/mp4", data: data}
			var commands []string
			var uploaded bytes.Buffer
			segment := 0
			client := Client{http: &http.Client{Transport: timelineTransport(func(r *http.Request) (*http.Response, error) {
				q := r.URL.Query()
				command := q.Get("command")
				commands = append(commands, command)
				method := http.MethodPost
				if command == "STATUS" {
					method = http.MethodGet
				}
				if r.URL.Host != "upload.x.com" || r.URL.Path != "/i/media/upload.json" || r.Method != method {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
				}
				body := `{"media_id_string":"456"}`
				switch command {
				case "INIT":
					if q.Get("total_bytes") != strconv.Itoa(len(data)) || q.Get("media_type") != "video/mp4" || q.Get("media_category") != "tweet_video" {
						t.Fatalf("wrong registration %v", q)
					}
				case "APPEND":
					if q.Get("media_id") != "456" || q.Get("segment_index") != strconv.Itoa(segment) {
						t.Fatalf("wrong segment %v", q)
					}
					reader, err := r.MultipartReader()
					if err != nil {
						t.Fatal(err)
					}
					part, err := reader.NextPart()
					if err != nil {
						t.Fatal(err)
					}
					if part.FormName() != "media" {
						t.Fatal("missing video part")
					}
					if _, err := io.Copy(&uploaded, part); err != nil {
						t.Fatal(err)
					}
					segment++
					body = ""
				case "FINALIZE":
					if q.Get("media_id") != "456" || !bytes.Equal(data, uploaded.Bytes()) {
						t.Fatal("finalized a different or incomplete upload")
					}
					body = `{"media_id_string":"456","processing_info":{"state":"in_progress","check_after_secs":0}}`
				case "STATUS":
					if q.Get("media_id") != "456" {
						t.Fatal("polled a different asset")
					}
					body = tc.final
				default:
					return nil, fmt.Errorf("unexpected command %q", command)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})}}
			id, err := client.uploadVideo(context.Background(), video)
			if (err != nil) != tc.wantError {
				t.Fatalf("id=%q err=%v", id, err)
			}
			if err == nil && id != "456" {
				t.Fatalf("unexpected media ID %q", id)
			}
			if strings.Join(commands, ",") != "INIT,APPEND,APPEND,FINALIZE,STATUS" {
				t.Fatalf("unexpected upload sequence %v", commands)
			}
		})
	}
}
