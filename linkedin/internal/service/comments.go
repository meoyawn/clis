package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"golang.org/x/net/html"
)

const CreateCommentPath = "/flagship-web/rsc-action/actions/server-request?sduiid=com.linkedin.sdui.comments.createComment"

// PrepareComment follows the observed own-profile comment composer. Binding
// keys and optimistic IDs are local identifiers, not copied account state.
func PrepareComment(ref domain.PostRef, draft domain.PostDraft) ([]byte, error) {
	if _, err := domain.NewPostDraft(draft.Text()); err != nil {
		return nil, err
	}
	if n := len(utf16.Encode([]rune(draft.Text()))); n > 1250 {
		return nil, fmt.Errorf("comment has %d UTF-16 code units; maximum is 1250", n)
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	suffix := fmt.Sprintf("cli-%x", random[:16])
	states, keys := []any{}, []any{}
	payload := map[string]any{
		"optimisticKey": "auto-component-" + suffix,
		"collection":    map[string]any{"updateKey": map[string]any{"feedType": 51, "items": []any{map[string]any{"feedUpdateUrn": map[string]any{"updateUrnActivityUrn": map[string]any{"activityUrn": map[string]any{"activityId": strings.TrimPrefix(ref.URN(), "urn:li:activity:")}}}, "trackingId": base64.StdEncoding.EncodeToString(random[16:])}}, "aggregationReasonTypes": []any{}, "isVideoCarousel": false}, "threadUrn": map[string]any{"threadUrnActivityThreadUrn": map[string]any{"activityUrn": map[string]any{"activityId": strings.TrimPrefix(ref.URN(), "urn:li:activity:")}}}},
	}
	for _, f := range []struct {
		name, key string
		kind      domain.PostBindingType
		value     any
	}{
		{"commentFieldBinding", "commentBoxText-", domain.PostStringBinding(), draft.Text()},
		{"richCommentFieldBinding", "richCommentBoxText-", domain.PostTextBinding(), map[string]any{"text": draft.Text(), "attribute": []any{}, "$type": "TextModel", "source": "local"}},
		{"linkPreviewIngestedContentId", "commentBoxLinkPreviewIngestedContentId-", domain.PostStringBinding(), ""},
		{"externalImageUrl", "commentBoxExternalImageUrl-", domain.PostStringBinding(), ""},
		{"externalImageId", "commentBoxExternalImageId-", domain.PostStringBinding(), ""},
		{"lastPasteCharacterCount", "commentBoxLastPasteCharacterCount-", domain.PostExpressionBinding(), 0},
		{"pasteToCreateDuration", "commentBoxPasteToCreateDuration-", domain.PostExpressionBinding(), map[string]string{"type": "bigint", "value": "0"}},
		{"hasEditsSinceLastPaste", "commentBoxHasEditsSinceLastPaste-", domain.PostBoolBinding(), false},
	} {
		key := f.key + suffix
		states = append(states, mediaState(key, f.kind, f.value))
		keys = append(keys, map[string]any{"key": map[string]any{"value": map[string]any{"$case": "id", "id": key}}})
		payload[f.name] = mediaBinding(key)
	}
	metadata := map[string]any{"$type": "proto.sdui.common.RequestMetadata", "currentActor": map[string]any{"$type": "proto.sdui.bindings.core.Bindable", "key": map[string]any{"key": map[string]any{"value": map[string]any{"$case": "id", "id": "identitySwitcherActorContext-" + ref.URN()}}, "namespace": ""}, "content": map[string]any{"$case": "stringBinding", "stringBinding": map[string]any{}}}}
	args := map[string]any{"$type": "proto.sdui.actions.requests.RequestedArguments", "requestedStateKeys": keys, "payload": payload, "requestMetadata": metadata}
	clientArgs := map[string]any{}
	for k, v := range args {
		clientArgs[k] = v
	}
	clientArgs["states"], clientArgs["screenId"], clientArgs["knownTemplateIds"] = states, "com.linkedin.sdui.flagshipnav.profile.ProfileRecentActivityAll", []any{}
	return json.Marshal(map[string]any{"requestId": "com.linkedin.sdui.comments.createComment", "serverRequest": map[string]any{"requestId": "com.linkedin.sdui.comments.createComment", "requestedArguments": args, "isApfcEnabled": true, "isStreaming": false, "rumPageKey": ""}, "states": states, "requestedArguments": clientArgs})
}

func ConfirmCommentCompletion(data []byte) error { _, err := rscCompletion(data); return err }

var commentKey = regexp.MustCompile(`^replaceableComment_urn:li:comment:\(activity:([1-9][0-9]*),([1-9][0-9]*)\)$`)

// ParsePostComments reads only the exact activity's rendered comment cards. The
// first page is not an exhaustive historical search.
func ParsePostComments(ref domain.PostRef, document string) ([]domain.Comment, error) {
	if _, err := ParsePostDocument(ref, document); err != nil {
		return nil, err
	}
	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return nil, err
	}
	comments := []domain.Comment{}
	seen := map[string]bool{}
	for n := range root.Descendants() {
		if n.Type != html.ElementNode || postHidden(n) {
			continue
		}
		m := commentKey.FindStringSubmatch(postAttr(n, "componentkey"))
		if len(m) != 3 || "urn:li:activity:"+m[1] != ref.URN() {
			continue
		}
		inPrimary := false
		for p := n.Parent; p != nil; p = p.Parent {
			if postAttr(p, "aria-label") == "Primary content" {
				inPrimary = true
				break
			}
		}
		if !inPrimary {
			continue
		}
		comment := domain.Comment{ID: m[2], URN: "urn:li:comment:(activity:" + m[1] + "," + m[2] + ")", ParentURN: ref.URN()}
		comment.URL = ref.URL() + "?dashCommentUrn=" + url.QueryEscape("urn:li:fsd_comment:("+m[2]+","+ref.URN()+")")
		var textNode *html.Node
		for child := range n.Descendants() {
			if child.Type != html.ElementNode || postHidden(child) {
				continue
			}
			if comment.AuthorURL == "" && child.Data == "a" && strings.TrimSpace(postText(child)) != "" {
				u, e := url.Parse(postAttr(child, "href"))
				if e == nil && u.Scheme == "https" && u.Host == "www.linkedin.com" && u.User == nil && strings.HasPrefix(u.Path, "/in/") {
					comment.AuthorURL = "https://www.linkedin.com" + strings.TrimRight(u.Path, "/") + "/"
				}
			}
			if postAttr(child, "data-testid") == "expandable-text-box" {
				// English comments can omit the translation wrapper. The
				// activity/comment-bound outer card remains authoritative;
				// never borrow text from a nested reply card.
				bound := true
				for p := child.Parent; p != nil && p != n; p = p.Parent {
					key := postAttr(p, "componentkey")
					if strings.HasPrefix(key, "replaceableComment_") || (strings.HasPrefix(key, "translatable-commentary-") && !strings.Contains(key, "commentId="+m[2]+",")) {
						bound = false
					}
				}
				if bound {
					if textNode != nil {
						return nil, fmt.Errorf("comment %q has multiple text blocks", comment.URN)
					}
					textNode = child
				}
			}
		}
		if textNode == nil || comment.AuthorURL == "" {
			return nil, fmt.Errorf("comment %q has no unambiguous author/text", comment.URN)
		}
		comment.Text = strings.TrimSpace(commentText(textNode))
		if seen[comment.ID] {
			return nil, fmt.Errorf("duplicate comment %q", comment.URN)
		}
		seen[comment.ID] = true
		comments = append(comments, comment)
	}
	return comments, nil
}

func commentText(n *html.Node) string {
	if postHidden(n) {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	if n.Type == html.ElementNode && n.Data == "br" {
		return "\n"
	}
	if n.Type == html.ElementNode && n.Data == "a" {
		u, err := url.Parse(postAttr(n, "href"))
		if err == nil && u.Scheme == "https" && u.User == nil {
			if u.Host == "www.linkedin.com" && u.Path == "/safety/go/" {
				if target := u.Query().Get("url"); target != "" {
					return target
				}
			}
			if u.Host != "www.linkedin.com" && u.Host != "linkedin.com" {
				return u.String()
			}
		}
	}
	var s strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		s.WriteString(commentText(child))
	}
	return s.String()
}
