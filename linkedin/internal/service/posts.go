package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/meoyawn/clis/linkedin/internal/domain"
)

// Captured on 2026-10-27 using Chrome request-stage interception. The
// CreatePost request was aborted before transmission; no live test was posted.
const CreatePostPath = "/flagship-web/rsc-action/actions/server-request?sduiid=com.linkedin.sdui.requests.sharing.CreatePost"

const createPostRequestID = "com.linkedin.sdui.requests.sharing.CreatePost"

// PreparePost builds the observed personal, public, plain-text post contract.
// Inline URLs are text; no link-preview, media, group, or scheduling is inferred.
func PreparePost(draft domain.PostDraft) ([]byte, error) {
	if _, err := domain.NewPostDraft(draft.Text()); err != nil {
		return nil, err
	}
	var sessionBytes [16]byte
	if _, err := rand.Read(sessionBytes[:]); err != nil {
		return nil, fmt.Errorf("generate sharebox session: %w", err)
	}
	states := []any{}
	keys := []any{}
	payload := map[string]any{}
	add := func(field, key string, protoCase domain.PostBindingType, value any) {
		bindingKey := map[string]any{"value": map[string]any{"$case": "id", "id": key}}
		keys = append(keys, map[string]any{"key": bindingKey})
		payload[field] = map[string]any{"key": key, "namespace": "MemoryNamespace"}
		states = append(states, map[string]any{
			"key": key, "namespace": "MemoryNamespace", "value": value, "originalProtoCase": protoCase.String(),
			"protoKey": map[string]any{"$type": "proto.sdui.Key", "value": bindingKey["value"]},
		})
	}
	add("richCommentaryTextBinding", "ShareBoxrichCommentaryTextMemoryBinding", domain.PostTextBinding(), map[string]any{
		"text": draft.Text(), "attribute": []any{}, "$type": "TextModel", "source": "local",
	})
	for _, field := range []struct{ name, key, value string }{
		{"detourType", "ShareBoxdetourType", "Text"},
		{"collectionRefBinding", "ShareBoxcollectionRefBinding", "mainFeed"},
		{"shareOrigin", "ShareBoxshareOriginBinding", ""},
		{"nbaProfileEntityUrnString", "ShareBoxnbaProfileEntityUrnBinding", ""},
		{"sourceReference", "ShareBoxsourceReferenceBinding", ""},
		{"shareLifeCycleState", "ShareBoxshareLifeCycleState", "ShareLifeCycleState_PUBLISHED"},
		{"visibilityType", "ShareBoxselectedVisibilitySettingString", "Anyone"},
		{"commentControlType", "ShareBoxselectedCommentControl", "Anyone"},
		{"actorContext", "ShareBoxselectedActor", "Self"},
		{"groupId", "ShareBoxselectedGroup", "-1"},
	} {
		add(field.name, field.key, domain.PostStringBinding(), field.value)
	}
	add("scheduledPublishTime", "ShareBoxscheduledPublishTime", domain.PostLongBinding(), map[string]string{"type": "bigint", "value": "0"})
	add("paidEndorsement", "ShareBoxbrandPartnerEnable", domain.PostBoolBinding(), false)
	add("parentUrnString", "ShareBoxparentUrnBinding", domain.PostStringBinding(), "")
	add("mediaUrnString", "ShareBoxmediaUrnBinding", domain.PostStringBinding(), "")
	add("shareboxSessionUrn", "ShareBoxshareboxSessionUrn", domain.PostStringBinding(), "urn:li:shareboxSession:"+base64.StdEncoding.EncodeToString(sessionBytes[:]))
	add("autoSaveGeneration", "ShareBoxautoSaveComposerGeneration", domain.PostExpressionBinding(), 2)
	add("shareBoxMode", "ShareBoxshareBoxMode", domain.PostStringBinding(), "Create")
	add("gifOriginalUrl", "ShareBoxgifUrl", domain.PostStringBinding(), "")
	add("repostingThreadUrnString", "ShareBoxrepostingThreadUrnBinding", domain.PostStringBinding(), "")
	add("collaboratorUrns", "ShareBoxcollaborators", domain.PostStringListBinding(), []any{})
	for _, field := range []struct{ name, key string }{
		{"languageIds", "Language"}, {"regionIds", "Region"}, {"locationIds", "Location"},
		{"functionIds", "Function"}, {"organizationIds", "School"}, {"companySizeIds", "CompanySize"},
		{"seniorityIds", "Seniority"}, {"industryIds", "Industry"},
	} {
		add(field.name, "PagesTargetAudience"+field.key+"-selectedCriteriaIds", domain.PostStringListBinding(), []any{})
	}
	metadata := map[string]any{
		"$type": "proto.sdui.common.RequestMetadata", "currentActor": map[string]any{
			"$type":   "proto.sdui.bindings.core.Bindable",
			"key":     map[string]any{"key": map[string]any{"value": map[string]any{"$case": "id", "id": "ShareBoxselectedActor"}}, "namespace": ""},
			"content": map[string]any{"$case": "stringBinding", "stringBinding": map[string]any{}},
		},
	}
	args := map[string]any{"$type": "proto.sdui.actions.requests.RequestedArguments", "requestedStateKeys": keys, "payload": payload, "requestMetadata": metadata}
	clientArgs := map[string]any{}
	for k, v := range args {
		clientArgs[k] = v
	}
	clientArgs["states"], clientArgs["screenId"], clientArgs["knownTemplateIds"] = states, "com.linkedin.sdui.flagshipnav.sharing.ShareCompose", []any{}
	return json.Marshal(map[string]any{
		"requestId": createPostRequestID, "serverRequest": map[string]any{
			"requestId": createPostRequestID, "requestedArguments": args, "isApfcEnabled": false, "isStreaming": false, "rumPageKey": "",
		}, "states": states, "requestedArguments": clientArgs,
	})
}

// PostReceipt fails closed: HTTP 200 or a generic completion is insufficient.
// An error-free RSC completion must identify exactly one activity permalink.
func PostReceipt(data []byte) (domain.PostRef, error) {
	for _, line := range strings.Split(string(data), "\n") {
		payload, ok := strings.CutPrefix(line, "0:")
		if !ok {
			continue
		}
		var envelope struct {
			Response *struct {
				Errors           *[]json.RawMessage `json:"errors"`
				CompletionAction any                `json:"completionAction"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			return domain.PostRef{}, fmt.Errorf("decode CreatePost receipt: %w", err)
		}
		if envelope.Response == nil || envelope.Response.Errors == nil || len(*envelope.Response.Errors) != 0 || envelope.Response.CompletionAction == nil {
			return domain.PostRef{}, fmt.Errorf("CreatePost did not return an error-free completion")
		}
		found := map[string]domain.PostRef{}
		var visit func(any)
		visit = func(value any) {
			switch value := value.(type) {
			case map[string]any:
				// Current completions pass the new activity to follow-up feed
				// actions as a numeric activityId instead of a permalink.
				if id, ok := value["activityId"].(string); ok {
					if ref, err := domain.NewPostRef("urn:li:activity:" + id); err == nil {
						found[ref.URN()] = ref
					}
				}
				for _, child := range value {
					visit(child)
				}
			case []any:
				for _, child := range value {
					visit(child)
				}
			case string:
				if ref, err := domain.NewPostRef(value); err == nil {
					found[ref.URN()] = ref
				}
			}
		}
		visit(envelope.Response.CompletionAction)
		if len(found) != 1 {
			return domain.PostRef{}, fmt.Errorf("CreatePost completion identifies %d activities; inspect the saved receipt and your profile before retrying", len(found))
		}
		for _, ref := range found {
			return ref, nil
		}
	}
	return domain.PostRef{}, fmt.Errorf("CreatePost response has no RSC completion receipt")
}
