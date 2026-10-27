package service

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	_ "golang.org/x/image/webp"
)

type PostImage struct {
	Path   string `json:"path"`
	MIME   string `json:"mime_type"`
	Size   int    `json:"bytes"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	SHA256 string `json:"sha256"`
	data   []byte
}

func LoadPostImage(path string) (*PostImage, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open image %q: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("image %q must be a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, (5<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > 5<<20 {
		return nil, fmt.Errorf("image %q must contain 1–5242880 bytes", path)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image %q: %w", path, err)
	}
	if format != "png" && format != "jpeg" && format != "webp" {
		return nil, fmt.Errorf("image %q has unsupported format %q; use PNG, JPEG or WebP", path, format)
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 36_000_000 {
		return nil, fmt.Errorf("image %q has unsupported dimensions %dx%d", path, config.Width, config.Height)
	}
	return &PostImage{Path: path, MIME: "image/" + format, Size: len(data), Width: config.Width, Height: config.Height, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), data: data}, nil
}
func (i *PostImage) Reader() io.Reader { return bytes.NewReader(i.data) }

type ImageUpload struct {
	Image            *PostImage
	fileID, uploadID string
}

func NewImageUpload(img *PostImage) (ImageUpload, error) {
	if img == nil {
		return ImageUpload{}, fmt.Errorf("image upload requires an image")
	}
	var ids [32]byte
	if _, err := rand.Read(ids[:]); err != nil {
		return ImageUpload{}, err
	}
	uuid := func(b []byte) string { return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]) }
	return ImageUpload{Image: img, fileID: uuid(ids[:16]), uploadID: "imageUpload-" + uuid(ids[16:])}, nil
}

func mediaState(key string, kind domain.PostBindingType, value any) map[string]any {
	return map[string]any{"key": key, "namespace": "MemoryNamespace", "value": value, "originalProtoCase": kind.String(), "protoKey": map[string]any{"$type": "proto.sdui.Key", "value": map[string]any{"$case": "id", "id": key}}}
}
func mediaBinding(key string) map[string]any {
	return map[string]any{"key": key, "namespace": "MemoryNamespace"}
}
func (u ImageUpload) mediaList() map[string]any {
	name := filepath.Base(u.Image.Path)
	return map[string]any{"$type": "proto.sdui.media.MediaList", "mediaFiles": []any{map[string]any{
		"mediaAttributions": []any{}, "analysisData": map[string]any{}, "fileName": name, "source": map[string]any{"$case": "localFile", "localFile": map[string]any{"fileName": name, "mimeType": u.Image.MIME, "size": fmt.Sprint(u.Image.Size), "filePointer": u.fileID}}, "size": fmt.Sprint(u.Image.Size), "mimeType": u.Image.MIME, "filePointer": u.fileID, "mediaFileId": u.fileID, "editData": map[string]any{"imageFilter": "ImageFilter_UNKNOWN", "editedMediaWidth": u.Image.Width, "editedMediaHeight": u.Image.Height}, "captions": []any{}, "thumbnails": []any{},
	}}}
}
func mediaRequest(action domain.ImageAction, states []any, payload map[string]any) ([]byte, error) {
	keys := []any{}
	for _, s := range states {
		key := s.(map[string]any)["key"]
		keys = append(keys, map[string]any{"key": map[string]any{"value": map[string]any{"$case": "id", "id": key}}})
	}
	metadata := map[string]any{"$type": "proto.sdui.common.RequestMetadata", "currentActor": map[string]any{"$type": "proto.sdui.bindings.core.Bindable", "key": map[string]any{"key": map[string]any{"value": map[string]any{"$case": "id", "id": "ShareBoxselectedActor"}}, "namespace": ""}, "content": map[string]any{"$case": "stringBinding", "stringBinding": map[string]any{}}}}
	args := map[string]any{"$type": "proto.sdui.actions.requests.RequestedArguments", "requestedStateKeys": keys, "payload": payload, "requestMetadata": metadata}
	clientArgs := map[string]any{}
	for k, v := range args {
		clientArgs[k] = v
	}
	clientArgs["states"] = states
	clientArgs["screenId"] = "com.linkedin.sdui.flagshipnav.sharing.ShareCompose"
	clientArgs["knownTemplateIds"] = []any{}
	return json.Marshal(map[string]any{"requestId": action.String(), "serverRequest": map[string]any{"requestId": action.String(), "requestedArguments": args, "isApfcEnabled": false, "isStreaming": false, "rumPageKey": ""}, "states": states, "requestedArguments": clientArgs})
}
func (u ImageUpload) PrepareUpload() ([]byte, error) {
	states := []any{mediaState("media-creation-editor-pickedMedia", domain.PostMediaListBinding(), u.mediaList()), mediaState(u.uploadID, domain.PostStringBinding(), "None"), mediaState("media-creation-editor-imageAssetIds", domain.PostStringListBinding(), []string{})}
	return mediaRequest(domain.ImageUploadAction(), states, map[string]any{"uploadId": u.uploadID, "mediaList": mediaBinding("media-creation-editor-pickedMedia"), "uploadState": mediaBinding(u.uploadID), "assetIds": mediaBinding("media-creation-editor-imageAssetIds"), "imageAssetUseCase": "ImageAssetUseCase_FEEDSHARE_IMAGE", "transient": false})
}

var assetIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{10,100}$`)

func (u ImageUpload) PrepareRegistration(assetID string) ([]byte, error) {
	if !assetIDPattern.MatchString(assetID) {
		return nil, fmt.Errorf("invalid image asset ID %q", assetID)
	}
	states := []any{mediaState("media-creation-editor-imageAssetIds", domain.PostStringListBinding(), []string{assetID}), mediaState("media-creation-editor-pickedMedia", domain.PostMediaListBinding(), u.mediaList()), mediaState("ShareBoxselectedActor", domain.PostStringBinding(), "Self")}
	return mediaRequest(domain.ImageRegisterAction(), states, map[string]any{"assetIds": mediaBinding("media-creation-editor-imageAssetIds"), "mediaFiles": mediaBinding("media-creation-editor-pickedMedia"), "actorContext": mediaBinding("ShareBoxselectedActor")})
}

func rscCompletion(data []byte) (map[string]any, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if raw, ok := strings.CutPrefix(line, "0:"); ok {
			var root struct {
				Response *struct {
					Errors     *[]json.RawMessage `json:"errors"`
					Completion map[string]any     `json:"completionAction"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(raw), &root); err != nil {
				return nil, fmt.Errorf("decode RSC response: %w", err)
			}
			if root.Response == nil || root.Response.Errors == nil || len(*root.Response.Errors) != 0 || root.Response.Completion == nil {
				return nil, fmt.Errorf("RSC response did not confirm an error-free completion")
			}
			return root.Response.Completion, nil
		}
	}
	return nil, fmt.Errorf("RSC response has no completion")
}
func walkMedia(value any, visit func(map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		visit(v)
		for _, child := range v {
			walkMedia(child, visit)
		}
	case []any:
		for _, child := range v {
			walkMedia(child, visit)
		}
	}
}

type ImageUploadInstruction struct {
	AssetID, Path string
	Headers       http.Header
}

func ParseImageUpload(data []byte) (ImageUploadInstruction, error) {
	completion, err := rscCompletion(data)
	if err != nil {
		return ImageUploadInstruction{}, err
	}
	var assets, urls []string
	headers := http.Header{}
	walkMedia(completion, func(m map[string]any) {
		if m["$type"] == "proto.sdui.actions.core.media.UploadInstruction" {
			if id, ok := m["assetUrn"].(string); ok {
				assets = append(assets, id)
			}
		}
		if m["$type"] == "proto.com.linkedin.digitalmedia.ingest.SingleRequestUploadInstruction" {
			if target, ok := m["uploadUrl"].(string); ok {
				urls = append(urls, target)
			}
		}
		if m["$type"] == "proto.com.linkedin.digitalmedia.ingest.Header" && m["key"] == "mediaTypeFamily" && m["value"] == "STILLIMAGE" {
			headers.Set("mediaTypeFamily", "STILLIMAGE")
		}
	})
	if len(assets) != 1 || len(urls) != 1 || !assetIDPattern.MatchString(assets[0]) || headers.Get("mediaTypeFamily") != "STILLIMAGE" {
		return ImageUploadInstruction{}, fmt.Errorf("image initialization did not confirm exactly one still-image upload")
	}
	u, err := url.Parse(urls[0])
	if err != nil || u.Scheme != "https" || u.Host != "www.linkedin.com" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/dms-uploads/") || !strings.Contains(u.Path, "/"+assets[0]+"/") {
		return ImageUploadInstruction{}, fmt.Errorf("image initialization returned an unsupported upload destination")
	}
	return ImageUploadInstruction{AssetID: assets[0], Path: u.RequestURI(), Headers: headers}, nil
}
func ConfirmImageRegistration(data []byte, assetID string) error {
	completion, err := rscCompletion(data)
	if err != nil {
		return err
	}
	confirmed := false
	walkMedia(completion, func(m map[string]any) {
		if m["$type"] != "proto.sdui.actions.core.SetState" {
			return
		}
		value, _ := m["value"].(map[string]any)
		state, _ := value["state"].(map[string]any)
		key, _ := state["key"].(map[string]any)
		inner, _ := key["key"].(map[string]any)
		id, _ := inner["value"].(map[string]any)
		v, _ := state["value"].(map[string]any)
		if id["id"] == "ShareBoxassetIdBinding" && v["stringValue"] == assetID {
			confirmed = true
		}
	})
	if !confirmed {
		return fmt.Errorf("image registration did not confirm the uploaded asset")
	}
	return nil
}

func PrepareImagePost(draft domain.PostDraft, assetID string) ([]byte, error) {
	if !assetIDPattern.MatchString(assetID) {
		return nil, fmt.Errorf("invalid image asset ID %q", assetID)
	}
	base, err := PreparePost(draft)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(base, &root); err != nil {
		return nil, err
	}
	states := []any{}
	for _, s := range root["states"].([]any) {
		m := s.(map[string]any)
		if m["key"] == "ShareBoxscheduledPublishTime" {
			continue
		}
		if m["key"] == "ShareBoxdetourType" {
			m["value"] = "Image"
		}
		if m["key"] == "ShareBoxautoSaveComposerGeneration" {
			m["value"] = 1
		}
		states = append(states, m)
	}
	states = append(states, mediaState("ShareBoxassetIdBinding", domain.PostStringBinding(), assetID))
	root["states"] = states
	clientArgs := root["requestedArguments"].(map[string]any)
	serverArgs := root["serverRequest"].(map[string]any)["requestedArguments"].(map[string]any)
	for _, args := range []map[string]any{clientArgs, serverArgs} {
		keys := args["requestedStateKeys"].([]any)
		payload := args["payload"].(map[string]any)
		for _, field := range []struct{ name, key string }{{"mediaAssetIdLong", "ShareBoxassetIdLongBinding"}, {"mediaLiveVideoIdLong", "ShareBoxliveVideoIdBinding"}, {"mediaAssetId", "ShareBoxassetIdBinding"}, {"celebrationOccasionType", "SharingCelebrationDetourselectedOccasion"}, {"mediaTitle", "ShareBoxmediaTitleBinding"}} {
			keys = append(keys, map[string]any{"key": map[string]any{"value": map[string]any{"$case": "id", "id": field.key}}})
			payload[field.name] = mediaBinding(field.key)
		}
		payload["taggedEntities"] = []any{}
		args["requestedStateKeys"] = keys
	}
	clientArgs["states"] = states
	return json.Marshal(root)
}
