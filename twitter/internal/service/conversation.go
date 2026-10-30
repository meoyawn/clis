package service

import (
	"context"
	"fmt"
	"slices"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

func (c *Client) ReadConversation(ctx context.Context, ref domain.PostRef) (domain.Conversation, error) {
	records, err := c.threadRecords(ctx, ref)
	if err != nil {
		return domain.Conversation{}, err
	}
	post, err := postFromRecords(records, ref)
	if err != nil {
		return domain.Conversation{}, err
	}
	conversation := domain.Conversation{Post: post, Parents: []domain.Post{}, Replies: []domain.Post{}}
	byID := make(map[string][]tweetRecord, len(records))
	for _, record := range records {
		byID[record.ID] = append(byID[record.ID], record)
	}
	// Follow reply links only within this response. Missing ancestors never
	// trigger another request, and unrelated conversation branches stay out.
	seenParents := map[string]bool{post.ID: true}
	for id := post.ReplyTo; id != ""; {
		if seenParents[id] {
			return domain.Conversation{}, fmt.Errorf("conversation for post %q contains a parent cycle at %q", post.ID, id)
		}
		seenParents[id] = true
		parentRef, err := domain.NewPostRef(id)
		if err != nil {
			return domain.Conversation{}, fmt.Errorf("post %q has invalid parent %q: %w", post.ID, id, err)
		}
		if len(byID[id]) == 0 {
			conversation.MissingParentID = id
			break
		}
		parent, err := postFromRecords(byID[id], parentRef)
		if err != nil {
			return domain.Conversation{}, fmt.Errorf("read parent %q of post %q: %w", id, post.ID, err)
		}
		conversation.Parents = append(conversation.Parents, parent)
		id = parent.ReplyTo
	}
	conversation.ParentsComplete = conversation.MissingParentID == ""
	slices.Reverse(conversation.Parents)
	seenReplies := map[string]bool{}
	for _, record := range records {
		if record.Legacy.ReplyTo != post.ID || seenReplies[record.ID] {
			continue
		}
		reply, err := record.post()
		if err != nil {
			return domain.Conversation{}, err
		}
		seenReplies[reply.ID] = true
		conversation.Replies = append(conversation.Replies, reply)
	}
	return conversation, nil
}
