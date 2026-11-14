package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"

	"github.com/meoyawn/clis/linkedin/internal/domain"
)

type PostVideo struct {
	Path   string `json:"path"`
	MIME   string `json:"mime_type"`
	Size   int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	data   []byte
}

func LoadPostVideo(path string) (*PostVideo, error) {
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
	if !info.Mode().IsRegular() || info.Size() < 75_000 || info.Size() > 512<<20 {
		return nil, fmt.Errorf("video %q must be a regular MP4 file between 75000 bytes and 512 MiB", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, (512<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 512<<20 || http.DetectContentType(data) != "video/mp4" {
		return nil, fmt.Errorf("video %q must be MP4 and at most 512 MiB", path)
	}
	return &PostVideo{Path: path, MIME: "video/mp4", Size: len(data), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), data: data}, nil
}

func (v *PostVideo) Reader(first, last int) (io.Reader, error) {
	if first < 0 || last < first || last >= len(v.data) {
		return nil, fmt.Errorf("video byte range %d–%d exceeds %d frozen bytes", first, last, len(v.data))
	}
	return bytes.NewReader(v.data[first : last+1]), nil
}

var videoURNPattern = regexp.MustCompile(`^urn:li:digitalmediaAsset:[A-Za-z0-9_-]+$`)
var activityInVideoReceipt = regexp.MustCompile(`urn:li:activity:[0-9]+`)

// Observed in the native VIDEO interop sharebox's post-create GraphQL module.
const CreateVideoPostPath = "/voyager/api/graphql?action=execute&queryId=voyagerContentcreationDashShares.ab2036c0a5ac89913595aa11ac5a26f4"

func PrepareVideoPost(draft domain.PostDraft, urn string, recipes []string) ([]byte, error) {
	if !videoURNPattern.MatchString(urn) || len(recipes) == 0 {
		return nil, fmt.Errorf("video registration lacks a valid asset URN or recipes")
	}
	return json.Marshal(map[string]any{
		"variables": map[string]any{"post": map[string]any{
			"allowedCommentersScope":      "ALL",
			"intendedShareLifeCycleState": "PUBLISHED",
			"origin":                      "FEED",
			"visibilityDataUnion":         map[string]any{"visibilityType": "ANYONE"},
			"commentary":                  map[string]any{"text": draft.Text(), "attributesV2": []any{}},
			"media": map[string]any{
				"category": "VIDEO", "mediaUrn": urn, "recipes": recipes, "nativeMediaSource": "PRE_RECORDED",
			},
		}},
		"queryId": "voyagerContentcreationDashShares.ab2036c0a5ac89913595aa11ac5a26f4",
	})
}

func VideoPostReceipt(data []byte) (domain.PostRef, error) {
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(data, &envelope) == nil && len(envelope.Value) != 0 {
		data = envelope.Value
	}
	var response struct {
		Errors []any           `json:"errors"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &response) != nil || len(response.Errors) != 0 || len(response.Data) == 0 {
		return domain.PostRef{}, fmt.Errorf("video post response has errors or no data")
	}
	refs := map[string]bool{}
	for _, urn := range activityInVideoReceipt.FindAllString(string(response.Data), -1) {
		refs[urn] = true
	}
	if len(refs) != 1 {
		return domain.PostRef{}, fmt.Errorf("video post receipt identifies %d activities", len(refs))
	}
	for urn := range refs {
		return domain.NewPostRef(urn)
	}
	return domain.PostRef{}, fmt.Errorf("video post receipt has no activity")
}
