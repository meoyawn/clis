package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

type MessagePermissions struct {
	CanMessage         *bool  `json:"can_message"`
	CanDM              *bool  `json:"can_dm"`
	CanDMOnXChat       *bool  `json:"can_dm_on_xchat"`
	Reason             string `json:"reason,omitempty"`
	DMBlocking         *bool  `json:"dm_blocking"`
	PassesPremiumCheck *bool  `json:"passes_premium_check"`
}

type MessagingAvailability struct {
	Recipient FollowState `json:"recipient"`
	MessagePermissions
}

var chatCoreAsset = regexp.MustCompile(`^assets/dm-core-[A-Za-z0-9_-]+\.js$`)

// Discover the current query from the public Chat manifest and core module.
// Neither is authenticated, and their JavaScript is inspected, never executed.
func (c *Client) messagingQueryID(ctx context.Context) (string, error) {
	return c.chatOperationID(ctx, domain.ChatUsersOperation())
}

func (c *Client) chatOperationID(ctx context.Context, operation domain.Operation) (string, error) {
	module := c.chatModule
	if module == nil {
		var err error
		module, err = c.loadChatModule(ctx)
		if err != nil {
			return "", err
		}
		c.chatModule = module
	}
	metadata := regexp.MustCompile("this\\.[A-Za-z0-9_$]+=`([A-Za-z0-9_-]+)`,this\\.[A-Za-z0-9_$]+=`" + regexp.QuoteMeta(operation.String()) + "`").FindAllSubmatch(module, -1)
	if len(metadata) != 1 {
		return "", fmt.Errorf("the X Chat module does not expose one %s operation", operation)
	}
	return string(metadata[0][1]), nil
}

func (c *Client) loadChatModule(ctx context.Context) ([]byte, error) {
	data, err := c.request(ctx, http.MethodGet, "https://chat.x.com/embed-v1.json", "", nil)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Metadata struct {
			PublicPath string `json:"publicPath"`
		} `json:"metaData"`
		Exposes []struct {
			Assets struct {
				JS struct {
					Async []string `json:"async"`
					Sync  []string `json:"sync"`
				} `json:"js"`
			} `json:"assets"`
		} `json:"exposes"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode X Chat public manifest: %w", err)
	}
	if manifest.Metadata.PublicPath != "https://abs.twimg.com/x-web/xchat/embed/" {
		return nil, fmt.Errorf("unsupported X Chat public asset origin %q", manifest.Metadata.PublicPath)
	}
	core := ""
	for _, expose := range manifest.Exposes {
		for _, asset := range append(expose.Assets.JS.Async, expose.Assets.JS.Sync...) {
			if !chatCoreAsset.MatchString(asset) {
				continue
			}
			if core != "" && core != asset {
				return nil, fmt.Errorf("the X Chat manifest contains conflicting core assets")
			}
			core = asset
		}
	}
	if core == "" {
		return nil, fmt.Errorf("the X Chat manifest does not expose its core asset")
	}
	return c.request(ctx, http.MethodGet, manifest.Metadata.PublicPath+core, "", nil)
}

// MessagingAvailability is read-only. It uses Chat's recipient-specific
// permissions, not profile buttons or a relationship-based inference.
func (c *Client) MessagingAvailability(ctx context.Context, handle string) (MessagingAvailability, error) {
	state, err := c.Following(ctx, handle)
	if err != nil {
		return MessagingAvailability{}, err
	}
	if err := c.PrepareWriteMetadata(ctx); err != nil {
		return MessagingAvailability{}, err
	}
	queryID, err := c.messagingQueryID(ctx)
	if err != nil {
		return MessagingAvailability{}, err
	}
	variables, err := json.Marshal(map[string]any{"ids": []string{state.Profile.ID}, "with_social_proof": false})
	if err != nil {
		return MessagingAvailability{}, err
	}
	query := url.Values{"variables": {string(variables)}}
	data, err := c.request(ctx, http.MethodGet, "https://api.x.com/graphql/"+queryID+"/GetUsersByIdsForXChat?"+query.Encode(), "", nil, http.Header{"Apollo-Require-Preflight": {"true"}})
	if err != nil {
		return MessagingAvailability{}, err
	}
	if err := graphQLError(data); err != nil {
		return MessagingAvailability{}, err
	}
	var response struct {
		Data struct {
			Members struct {
				Results []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Member struct {
						ID     string `json:"rest_id"`
						Result struct {
							userRecord
							Permissions *struct {
								CanDM              *bool  `json:"can_dm"`
								CanDMOnXChat       *bool  `json:"can_dm_on_xchat"`
								Reason             string `json:"can_dm_reason"`
								DMBlocking         *bool  `json:"dm_blocking"`
								PassesPremiumCheck *bool  `json:"passes_premium_check"`
							} `json:"chat_permissions"`
						} `json:"result"`
					} `json:"member_results"`
				} `json:"results"`
			} `json:"get_member_results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return MessagingAvailability{}, fmt.Errorf("decode X Chat permissions: %w", err)
	}
	members := response.Data.Members.Results
	if len(members) != 1 {
		return MessagingAvailability{}, fmt.Errorf("the X Chat returned %d members for %q", len(members), handle)
	}
	member := members[0]
	if member.Status != "Found" || member.ID != state.Profile.ID || member.Member.ID != state.Profile.ID {
		return MessagingAvailability{}, fmt.Errorf("the X Chat did not confirm exact recipient %q", handle)
	}
	profile, err := member.Member.Result.profile()
	if err != nil {
		return MessagingAvailability{}, err
	}
	if profile.ID != state.Profile.ID || !strings.EqualFold(profile.Handle, state.Profile.Handle) {
		return MessagingAvailability{}, fmt.Errorf("the X Chat returned a different identity for %q", handle)
	}
	result := MessagingAvailability{Recipient: state}
	if p := member.Member.Result.Permissions; p != nil {
		result.MessagePermissions = MessagePermissions{CanDM: p.CanDM, CanDMOnXChat: p.CanDMOnXChat, Reason: p.Reason, DMBlocking: p.DMBlocking, PassesPremiumCheck: p.PassesPremiumCheck}
	}
	// Chat checks the recipient's reason and Premium restriction separately.
	// can_dm_on_xchat describes encrypted Chat support, not ordinary DM access.
	p := result.MessagePermissions
	denied := state.Blocking || state.BlockedBy ||
		(p.CanDM != nil && !*p.CanDM) ||
		(p.DMBlocking != nil && *p.DMBlocking) ||
		(p.PassesPremiumCheck != nil && !*p.PassesPremiumCheck)
	allowed := p.CanDM != nil && *p.CanDM && p.Reason == "Allowed" &&
		p.DMBlocking != nil && !*p.DMBlocking &&
		p.PassesPremiumCheck != nil && *p.PassesPremiumCheck
	if denied {
		value := false
		result.CanMessage = &value
	} else if allowed {
		value := true
		result.CanMessage = &value
	}
	return result, nil
}
