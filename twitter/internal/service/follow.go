package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

type FollowState struct {
	Profile   domain.Profile `json:"profile"`
	Following bool           `json:"following"`
	Requested bool           `json:"requested"`
	Protected bool           `json:"protected"`
	Blocking  bool           `json:"blocking"`
	BlockedBy bool           `json:"blocked_by"`
}

func (c *Client) Following(ctx context.Context, handle string) (FollowState, error) {
	handle, err := domain.Handle(handle)
	if err != nil {
		return FollowState{}, err
	}
	if c.owner.ID == "" {
		return FollowState{}, fmt.Errorf("follow state for %q requires a signed-in client", handle)
	}
	var results []json.RawMessage
	if c.redesigned {
		results, err = c.redesignedPage(ctx, "/"+handle)
	} else {
		var data []byte
		data, err = c.query(ctx, domain.ProfileOperation(), map[string]any{"screen_name": handle, "withGrokTranslatedBio": true}, map[string]bool{"withPayments": false, "withAuxiliaryUserLabels": true})
		results = []json.RawMessage{data}
	}
	if err != nil {
		return FollowState{}, err
	}
	profile, err := profileInResults(results, "", handle)
	if err != nil {
		return FollowState{}, err
	}
	if id, err := strconv.ParseUint(profile.ID, 10, 64); err != nil || id == 0 {
		return FollowState{}, fmt.Errorf("profile %q has invalid numeric ID %q", handle, profile.ID)
	}
	var found *FollowState
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			if value["__typename"] == "User" && value["rest_id"] == profile.ID {
				data, err := json.Marshal(value)
				if err != nil {
					return err
				}
				var user struct {
					Following *bool `json:"is_following"`
					Requested *bool `json:"follow_request_sent"`
					Privacy   struct {
						Protected *bool `json:"protected"`
					} `json:"privacy"`
					Relationship struct {
						Following *bool `json:"following"`
						Blocking  *bool `json:"blocking"`
						BlockedBy *bool `json:"blocked_by"`
					} `json:"relationship_perspectives"`
				}
				if err := json.Unmarshal(data, &user); err != nil {
					return err
				}
				if user.Following != nil && user.Relationship.Following != nil && *user.Following != *user.Relationship.Following {
					return fmt.Errorf("conflicting follow state for %q", handle)
				}
				if user.Following == nil {
					user.Following = user.Relationship.Following
				}
				if user.Following == nil || user.Requested == nil || user.Privacy.Protected == nil || user.Relationship.Blocking == nil || user.Relationship.BlockedBy == nil {
					return nil
				}
				state := FollowState{Profile: profile, Following: *user.Following, Requested: *user.Requested, Protected: *user.Privacy.Protected, Blocking: *user.Relationship.Blocking, BlockedBy: *user.Relationship.BlockedBy}
				if found != nil && *found != state {
					return fmt.Errorf("conflicting follow state for %q", handle)
				}
				found = &state
				return nil
			}
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, result := range results {
		var root any
		if err := json.Unmarshal(result, &root); err != nil {
			return FollowState{}, err
		}
		if err := walk(root); err != nil {
			return FollowState{}, err
		}
	}
	if found == nil {
		return FollowState{}, fmt.Errorf("profile %q does not expose complete follow state", handle)
	}
	return *found, nil
}

func (c *Client) Follow(ctx context.Context, target domain.Profile, receipt io.Writer) (FollowState, error) {
	before, err := c.Following(ctx, target.Handle)
	if err != nil {
		return FollowState{}, err
	}
	if before.Profile.ID != target.ID || target.ID == c.owner.ID {
		return FollowState{}, fmt.Errorf("follow target %q changed identity or is the selected account", target.Handle)
	}
	if before.Blocking || before.BlockedBy {
		return FollowState{}, fmt.Errorf("profile %q has a blocked relationship", target.Handle)
	}
	if before.Following || before.Requested {
		return before, nil
	}
	if err := c.PrepareWriteMetadata(ctx); err != nil {
		return FollowState{}, err
	}
	// Inspected x-web follow-user contract, 8 November 2026. A follow uses
	// only the resolved numeric ID; protected accounts create a request.
	body := url.Values{"user_id": {target.ID}}.Encode()
	data, requestErr := c.request(ctx, http.MethodPost, "https://api.x.com/1.1/friendships/create.json", "application/x-www-form-urlencoded", []byte(body))
	if receipt != nil {
		if _, err := receipt.Write(data); err != nil {
			return FollowState{}, fmt.Errorf("save follow receipt: %w", err)
		}
	}
	if requestErr != nil {
		return FollowState{}, requestErr
	}
	if err := graphQLError(data); err != nil {
		return FollowState{}, err
	}
	var response struct {
		ID string `json:"id_str"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return FollowState{}, err
	}
	if response.ID != target.ID {
		return FollowState{}, fmt.Errorf("follow receipt did not confirm target %q", target.ID)
	}
	after, err := c.Following(ctx, target.Handle)
	if err != nil {
		return FollowState{}, err
	}
	if after.Profile.ID != target.ID || !strings.EqualFold(after.Profile.Handle, target.Handle) || (!after.Following && !after.Requested) {
		return FollowState{}, fmt.Errorf("follow of %q was not confirmed by profile readback; do not retry automatically", target.Handle)
	}
	return after, nil
}
