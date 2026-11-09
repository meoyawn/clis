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
	if err := c.prepareDeletion(ctx); err != nil {
		return err
	}
	operation := domain.DeleteOperation().String()
	id := c.operations[operation]
	body, err := json.Marshal(map[string]any{"variables": map[string]string{"tweetId": post.ID}, "queryId": id})
	if err != nil {
		return err
	}
	data, err := c.request(ctx, http.MethodPost, "https://api.x.com/graphql/"+id+"/"+operation, "application/json", body)
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
		return fmt.Errorf("deletion receipt does not confirm post %q; inspect the saved response before retrying", post.ID)
	}
	return nil
}
