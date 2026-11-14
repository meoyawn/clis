package cli

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"github.com/meoyawn/clis/linkedin/internal/service"
	"golang.org/x/net/html"
)

func postMenuMatches(ref domain.PostRef, action map[string]any) bool {
	id := strings.TrimPrefix(ref.URN(), "urn:li:activity:")
	if jsonStringAt(action, "requestedArguments", "payload", "threadUrn", "threadUrnActivityThreadUrn", "activityUrn", "activityId") == id {
		return true
	}
	args, _ := action["requestedArguments"].(map[string]any)
	payload, _ := args["payload"].(map[string]any)
	key, _ := payload["updateKey"].(map[string]any)
	items, _ := key["items"].([]any)
	return len(items) == 1 && jsonStringAt(items[0], "feedUpdateUrn", "updateUrnActivityUrn", "activityUrn", "activityId") == id
}

func parsePostPage(ref domain.PostRef, document []byte) (domain.Post, error) {
	root, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return domain.Post{}, err
	}
	data := readDocumentData(root)
	thread, asset := ref.URN(), ""
	var conflict bool
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] != "proto.sdui.actions.core.ServerRequest" || jsonStringAt(m, "value", "requestId") != "feedUpdateControlMenuRequest" {
				return
			}
			action, _ := m["value"].(map[string]any)
			if !postMenuMatches(ref, action) {
				return
			}
			value := jsonStringAt(action, "requestedArguments", "payload", "rootUrn")
			if !strings.HasPrefix(value, "urn:li:ugcPost:") {
				value = ref.URN()
			}
			video := jsonStringAt(action, "requestedArguments", "payload", "videoViewerControlMenuViewData", "assetId")
			if value == "" || (thread != ref.URN() && thread != value) || (asset != "" && asset != video) {
				conflict = true
				return
			}
			thread, asset = value, video
		})
	}
	if conflict {
		return domain.Post{}, fmt.Errorf("post %q has conflicting native activity/thread bindings", ref.URL())
	}
	post, err := service.ParsePostDocumentThread(ref, string(document), thread)
	if err != nil {
		return post, err
	}
	if thread != ref.URN() {
		post.ThreadURN = thread
	}
	if asset != "" {
		post.VideoAssetIDs = []string{asset}
	}
	return post, nil
}

func parsePostPageComments(ref domain.PostRef, document []byte) ([]domain.Comment, error) {
	post, err := parsePostPage(ref, document)
	if err != nil {
		return nil, err
	}
	thread := post.ThreadURN
	if thread == "" {
		thread = ref.URN()
	}
	return service.ParsePostCommentsThread(ref, string(document), thread)
}
