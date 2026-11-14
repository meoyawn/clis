package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

func (c *Client) DeletePost(ctx context.Context, post domain.Post, receipt io.Writer) error {
	if c.owner.ID == "" || post.Author.ID != c.owner.ID {
		return fmt.Errorf("post %q does not belong to the selected account", post.ID)
	}
	if _, err := domain.NewPostRef(post.ID); err != nil {
		return err
	}
	if err := c.PrepareDeletionMetadata(ctx); err != nil {
		return err
	}
	operation := domain.DeleteOperation().String()
	endpoint := "https://api.x.com/graphql/"
	variables := map[string]any{"tweetId": post.ID}
	if !c.redesigned {
		operation = domain.LegacyDeleteOperation().String()
		endpoint = Origin + "/i/api/graphql/"
		variables = map[string]any{"tweet_id": post.ID, "dark_request": false}
	}
	id := c.operations[operation]
	body, err := json.Marshal(map[string]any{"variables": variables, "queryId": id})
	if err != nil {
		return err
	}
	data, err := c.request(ctx, http.MethodPost, endpoint+id+"/"+operation, "application/json", body)
	if receipt != nil {
		if _, writeErr := receipt.Write(data); writeErr != nil {
			return fmt.Errorf("save deletion receipt after transmission: %w", writeErr)
		}
	}
	if err != nil {
		return err
	}
	if err := graphQLError(data); err != nil {
		return err
	}
	var result struct {
		Data struct {
			Deleted struct {
				Results struct {
					ID string `json:"rest_id"`
				} `json:"tweet_results"`
			} `json:"delete_tweet"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode deletion receipt: %w", err)
	}
	if result.Data.Deleted.Results.ID != post.ID {
		if !c.redesigned && result.Data.Deleted.Results.ID == "" {
			var acknowledgement struct {
				Data struct {
					Deleted map[string]json.RawMessage `json:"delete_tweet"`
				} `json:"data"`
			}
			if json.Unmarshal(data, &acknowledgement) == nil && acknowledgement.Data.Deleted["tweet_results"] != nil {
				ref, _ := domain.NewPostRef(post.ID)
				records, readErr := c.threadRecords(ctx, ref)
				if readErr != nil {
					return fmt.Errorf("deletion acknowledged but readback failed; do not resend: %w", readErr)
				}
				for _, record := range records {
					if record.ID == post.ID {
						return fmt.Errorf("deletion acknowledged but post %q remains visible", post.ID)
					}
				}
				return nil
			}
		}
		return fmt.Errorf("deletion receipt does not confirm post %q; inspect the saved response before retrying", post.ID)
	}
	return nil
}
