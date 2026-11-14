package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"github.com/meoyawn/clis/linkedin/internal/service"
)

func TestVideoPreparationAndPublication(t *testing.T) {
	media := make([]byte, 80000)
	binary.BigEndian.PutUint32(media, 24)
	copy(media[4:], "ftypisom")
	copy(media[16:], "isommp42")
	path := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(path, media, 0600); err != nil {
		t.Fatal(err)
	}
	video, err := service.LoadPostVideo(path)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := domain.NewPostDraft("Line one\n\nRead https://example.com/article")
	if err != nil {
		t.Fatal(err)
	}
	const urn = "urn:li:digitalmediaAsset:VideoFixture"
	var uploads, publications int
	var uploaded bytes.Buffer
	client := linkedinClient{session: session{LIAT: "fixture", JSessionID: "ajax:fixture"}}
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		client.lastRequestAt = time.Time{}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/voyager/api/me":
			return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoSelf"}}`)), nil
		case r.Method == http.MethodGet && r.URL.Path == "/voyager/api/identity/dash/profiles":
			return jsonResponse([]byte(`{"data":{"*elements":["urn:li:fsd_profile:ACoSelf"]},"included":[{"entityUrn":"urn:li:fsd_profile:ACoSelf","publicIdentifier":"example-author","firstName":"Example","lastName":"Author"}]}`)), nil
		case r.Method == http.MethodPost && r.URL.Query().Get("action") == "upload":
			uploads++
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["fileSize"] != float64(len(media)) || request["mediaUploadType"] != "VIDEO_SHARING" {
				t.Fatalf("incorrect registration: %v", request)
			}
			return jsonResponse(mustJSON(map[string]any{"value": videoRegistration{URN: urn, Type: "MULTIPART", Recipes: []string{"urn:li:digitalmediaRecipe:feedshare-video"}, MediaArtifactURN: "urn:li:digitalmediaMediaArtifact:fixture", Metadata: json.RawMessage(`{"token":"fixture"}`), Parts: []videoUploadPart{{URL: "https://www.linkedin.com/dms-uploads/part0", FirstByte: 0, LastByte: 39999}, {URL: "https://www.linkedin.com/dms-uploads/part1", FirstByte: 40000, LastByte: 79999}}}})), nil
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/dms-uploads/"):
			if _, err := io.Copy(&uploaded, r.Body); err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 201, Header: http.Header{"Etag": {r.URL.Path}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		case r.Method == http.MethodPost && r.URL.Query().Get("action") == "completeMultipartUpload":
			if !bytes.Equal(uploaded.Bytes(), media) {
				t.Fatal("finalized corrupt or incomplete video")
			}
			var request struct {
				CompleteUploadRequest struct {
					PartUploadResponses []struct {
						Headers map[string]string
					}
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			parts := request.CompleteUploadRequest.PartUploadResponses
			if len(parts) != 2 || parts[1].Headers["etag"] != "/dms-uploads/part1" {
				t.Fatal("multipart completion omitted upload receipts")
			}
			return jsonResponse([]byte(`{"value":{}}`)), nil
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/voyager/api/voyagerVideoDashMediaAssetStatus/"):
			return jsonResponse([]byte(`{"processingStatus":"READY"}`)), nil
		case r.Method == http.MethodPost && r.URL.Path == "/voyager/api/graphql":
			publications++
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if jsonStringAt(request, "variables", "post", "media", "mediaUrn") != urn || jsonStringAt(request, "variables", "post", "commentary", "text") != draft.Text() {
				t.Fatal("published different commentary or video")
			}
			return jsonResponse([]byte(`{"value":{"data":{"createContentcreationDashShares":{"entity":{"status":{"feedbackData":{"toastComponent":{"linkUrl":"` + postPermalink + `"}}}}}}}}`)), nil
		case r.Method == http.MethodGet && r.URL.String() == postPermalink:
			document := nativeVideoPostDocument("123456789")
			return jsonResponse([]byte(document)), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	})}
	opts := postCreateOptions{video: video, recordDir: t.TempDir()}
	preview, err := client.createPost(context.Background(), draft, opts)
	if err != nil || uploads != 0 || publications != 0 {
		t.Fatalf("preview caused writes: %v", err)
	}
	if preview.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(draft.Text()+"\x00video:"+video.SHA256))) {
		t.Fatal("preview did not bind frozen video bytes")
	}
	opts.prepareMedia, opts.expectedAuthor, opts.expectedSHA256 = true, postAuthorURL, preview.SHA256
	prepared, err := client.createPost(context.Background(), draft, opts)
	if err != nil || prepared.VideoURN != urn || prepared.Published || uploads != 1 || publications != 0 {
		t.Fatalf("prepare published or failed: %+v %v", prepared, err)
	}
	opts.prepareMedia, opts.publish = false, true
	for attempt := 0; attempt < 2; attempt++ {
		result, err := client.createPost(context.Background(), draft, opts)
		if err != nil || !result.Verified || !result.Published || uploads != 1 || publications != 1 {
			t.Fatalf("publication/reuse failed: %+v %v uploads=%d posts=%d", result, err, uploads, publications)
		}
		if attempt == 0 {
			data, err := os.ReadFile(result.RecordPath)
			if err != nil {
				t.Fatal(err)
			}
			var journal postAttempt
			if err := json.Unmarshal(data, &journal); err != nil {
				t.Fatal(err)
			}
			journal.Published, journal.PostURL = false, ""
			if err := savePostAttempt(result.RecordPath, journal); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func nativeVideoPostDocument(activityID string) string {
	payload := map[string]any{"rootUrn": "urn:li:ugcPost:987654321"}
	payload["updateKey"] = map[string]any{"items": []any{map[string]any{"feedUpdateUrn": map[string]any{"updateUrnActivityUrn": map[string]any{"activityUrn": map[string]any{"activityId": activityID}}}}}}
	payload["videoViewerControlMenuViewData"] = map[string]any{"assetId": "VideoFixture"}
	action, _ := json.Marshal(map[string]any{"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{"requestId": "feedUpdateControlMenuRequest", "requestedArguments": map[string]any{"payload": payload}}})
	chunks, _ := json.Marshal([]string{"0:" + string(action) + "\n"})
	return strings.Replace(postDocument, "ReactionFacepileCollection-urn:li:activity:123456789", "ReactionFacepileCollection-urn:li:ugcPost:987654321", 1) + `<script>window.__como_rehydration__ = ` + string(chunks) + `;</script>`
}

func TestVideoThreadMustBeBoundToRequestedActivity(t *testing.T) {
	ref, err := domain.NewPostRef("urn:li:activity:123456789")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsePostPage(ref, []byte(nativeVideoPostDocument("111111111"))); err == nil {
		t.Fatal("accepted a UGC thread bound to a different activity")
	}
	post, err := parsePostPage(ref, []byte(nativeVideoPostDocument("123456789")))
	if err != nil || post.ThreadURN != "urn:li:ugcPost:987654321" || len(post.VideoAssetIDs) != 1 || post.VideoAssetIDs[0] != "VideoFixture" {
		t.Fatalf("native video binding: %+v %v", post, err)
	}
	draft, _ := domain.NewPostDraft("https://example.com/product")
	body, err := service.PrepareCommentThread(ref, draft, post.ThreadURN)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if jsonStringAt(request, "serverRequest", "requestedArguments", "payload", "collection", "threadUrn", "threadUrnUgcPostThreadUrn", "userGeneratedContentPostUrn", "userGeneratedContentId") != "987654321" {
		t.Fatal("comment did not target the verified UGC thread")
	}
}

func TestVideoUploadRejectsUnsafeDestinationsAndByteRanges(t *testing.T) {
	for _, part := range []videoUploadPart{
		{URL: "https://example.com/dms-uploads/part", FirstByte: 0, LastByte: 99},
		{URL: "https://www.linkedin.com@evil.example/dms-uploads/part", FirstByte: 0, LastByte: 99},
		{URL: "https://www.linkedin.com/dms-uploads/part", FirstByte: 1, LastByte: 99},
		{URL: "https://www.linkedin.com/dms-uploads/part", FirstByte: 0, LastByte: 100},
	} {
		_, err := validateVideoParts(videoRegistration{Type: "MULTIPART", MediaArtifactURN: "fixture", Metadata: json.RawMessage(`{}`), Parts: []videoUploadPart{part}}, 100)
		if err == nil {
			t.Fatalf("accepted unsafe part %+v", part)
		}
	}
}

func TestPostDeleteRequiresOneExactActivity(t *testing.T) {
	ref, err := domain.NewPostRef("urn:li:activity:123456789")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"123456789", "987654321", ""} {
		item := map[string]any{"feedUpdateUrn": map[string]any{"updateUrnActivityUrn": map[string]any{"activityUrn": map[string]any{"activityId": id}}}}
		action := map[string]any{"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{"requestId": "com.linkedin.sdui.update.deletePost", "requestedArguments": map[string]any{"requestedStateKeys": []any{}, "payload": map[string]any{"updateKeyContainer": map[string]any{"items": []any{item}}}}}}
		encoded, err := json.Marshal(action)
		if err != nil {
			t.Fatal(err)
		}
		_, err = preparePostDelete(ref, append([]byte("0:"), encoded...))
		if (err == nil) != (id == "123456789") {
			t.Fatalf("target %q: %v", id, err)
		}
	}
}
