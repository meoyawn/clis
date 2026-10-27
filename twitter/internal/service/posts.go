package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

type tweetRecord struct {
	Type string `json:"__typename"`
	ID   string `json:"rest_id"`
	Core struct {
		User struct {
			Result userRecord `json:"result"`
		} `json:"user_results"`
	} `json:"core"`
	Legacy struct {
		ID        string `json:"id_str"`
		AuthorID  string `json:"user_id_str"`
		Text      string `json:"full_text"`
		CreatedAt string `json:"created_at"`
		ReplyTo   string `json:"in_reply_to_status_id_str"`
		Likes     int64  `json:"favorite_count"`
		Replies   int64  `json:"reply_count"`
		Reposts   int64  `json:"retweet_count"`
		Quotes    int64  `json:"quote_count"`
		Bookmarks int64  `json:"bookmark_count"`
		Entities  struct {
			URLs []struct {
				URL      string `json:"url"`
				Expanded string `json:"expanded_url"`
			} `json:"urls"`
		} `json:"entities"`
		Extended struct {
			Media []struct {
				ID  string `json:"id_str"`
				URL string `json:"url"`
			} `json:"media"`
		} `json:"extended_entities"`
	} `json:"legacy"`
	Views struct {
		Count string `json:"count"`
	} `json:"views"`
	Note struct {
		Result struct {
			Text string `json:"text"`
		} `json:"result"`
	} `json:"note_tweet_results"`
}

func (t tweetRecord) post() (domain.Post, error) {
	owner, err := t.Core.User.Result.profile()
	if err != nil {
		return domain.Post{}, err
	}
	if t.Type != "Tweet" || t.ID == "" || t.ID != t.Legacy.ID || owner.ID != t.Legacy.AuthorID {
		return domain.Post{}, fmt.Errorf("post response contains inconsistent activity or author identity")
	}
	text := t.Legacy.Text
	if t.Note.Result.Text != "" {
		text = t.Note.Result.Text
	}
	for _, entity := range t.Legacy.Entities.URLs {
		if entity.URL != "" && entity.Expanded != "" {
			text = strings.ReplaceAll(text, entity.URL, entity.Expanded)
		}
	}
	media := make([]string, 0, len(t.Legacy.Extended.Media))
	for _, m := range t.Legacy.Extended.Media {
		media = append(media, m.ID)
		// The server appends a media t.co token outside the author's text.
		if m.URL != "" {
			text = strings.TrimSuffix(strings.TrimRight(text, " \n"), " "+m.URL)
		}
	}
	var views *int64
	if n, err := strconv.ParseInt(t.Views.Count, 10, 64); err == nil && n >= 0 {
		views = &n
	}
	return domain.Post{ID: t.ID, URL: Origin + "/" + owner.Handle + "/status/" + t.ID, Author: owner, Text: html.UnescapeString(text), CreatedAt: t.Legacy.CreatedAt, ReplyTo: t.Legacy.ReplyTo, MediaIDs: media, Views: views, Likes: t.Legacy.Likes, Replies: t.Legacy.Replies, Reposts: t.Legacy.Reposts, Quotes: t.Legacy.Quotes, Bookmarks: t.Legacy.Bookmarks}, nil
}

// Stop at each top-level Tweet. A quoted tweet is evidence inside a post, not
// another timeline entry or a substitute for the requested focal post.
func tweetRecords(data []byte) ([]tweetRecord, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("decode post response: %w", err)
	}
	var records []tweetRecord
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			if value["__typename"] == "Tweet" {
				raw, err := json.Marshal(value)
				if err != nil {
					return err
				}
				var t tweetRecord
				if err := json.Unmarshal(raw, &t); err != nil {
					return err
				}
				records = append(records, t)
				return nil
			}
			for _, v := range value {
				if err := walk(v); err != nil {
					return err
				}
			}
		case []any:
			for _, v := range value {
				if err := walk(v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(root)
	return records, err
}

func (c *Client) threadRecords(ctx context.Context, ref domain.PostRef) ([]tweetRecord, error) {
	data, err := c.query(ctx, domain.ReadOperation(), map[string]any{"focalTweetId": ref.ID(), "referrer": "me", "with_rux_injections": false, "rankingMode": "Relevance", "includePromotedContent": true, "withCommunity": true, "withQuickPromoteEligibilityTweetFields": true, "withBirdwatchNotes": true, "withVoice": true}, map[string]bool{"withPayments": false, "withDmBlocks": false, "withArticleRichContentState": true, "withArticlePlainText": false, "withArticleSummaryText": true, "withArticleVoiceOver": true, "withGrokAnalyze": false, "withDisallowedReplyControls": false})
	if err != nil {
		return nil, err
	}
	if c.threadCapture != nil {
		if _, err := c.threadCapture.Write(data); err != nil {
			return nil, fmt.Errorf("save post response: %w", err)
		}
	}
	return tweetRecords(data)
}

func (c *Client) ReadPost(ctx context.Context, ref domain.PostRef) (domain.Post, error) {
	records, err := c.threadRecords(ctx, ref)
	if err != nil {
		return domain.Post{}, err
	}
	var found *domain.Post
	for _, t := range records {
		if t.ID == ref.ID() {
			p, err := t.post()
			if err != nil {
				return domain.Post{}, err
			}
			if found != nil {
				return domain.Post{}, fmt.Errorf("ambiguous focal post %q", ref.ID())
			}
			found = &p
		}
	}
	if found == nil {
		return domain.Post{}, fmt.Errorf("requested post %q is unavailable in the response", ref.ID())
	}
	return *found, nil
}

func (c *Client) OwnReplies(ctx context.Context, ref domain.PostRef) ([]domain.Post, error) {
	records, err := c.threadRecords(ctx, ref)
	if err != nil {
		return nil, err
	}
	parentVerified := false
	for _, t := range records {
		if t.ID == ref.ID() {
			p, err := t.post()
			if err != nil {
				return nil, err
			}
			if p.Author.ID != c.owner.ID {
				return nil, fmt.Errorf("post %q does not belong to the signed-in account", ref.ID())
			}
			parentVerified = true
		}
	}
	if !parentVerified {
		return nil, fmt.Errorf("conversation does not confirm parent %q", ref.ID())
	}
	posts := []domain.Post{}
	seen := map[string]bool{}
	{
		data, err := c.query(ctx, domain.RepliesOperation(), map[string]any{"userId": c.owner.ID, "count": 20, "includePromotedContent": true, "withCommunity": true, "withVoice": true}, map[string]bool{"withPayments": false, "withArticlePlainText": false})
		if err != nil {
			return nil, err
		}
		more, err := tweetRecords(data)
		if err != nil {
			return nil, err
		}
		records = append(records, more...)
	}
	for _, t := range records {
		if t.Legacy.ReplyTo != ref.ID() || t.Legacy.AuthorID != c.owner.ID || seen[t.ID] {
			continue
		}
		p, err := t.post()
		if err != nil {
			return nil, err
		}
		seen[p.ID] = true
		posts = append(posts, p)
	}
	return posts, nil
}

func (c *Client) OwnPosts(ctx context.Context) ([]domain.Post, error) {
	data, err := c.query(ctx, domain.TimelineOperation(), map[string]any{"userId": c.owner.ID, "count": 20, "includePromotedContent": true, "withQuickPromoteEligibilityTweetFields": true, "withVoice": true}, map[string]bool{"withPayments": false, "withArticlePlainText": false})
	if err != nil {
		return nil, err
	}
	records, err := tweetRecords(data)
	if err != nil {
		return nil, err
	}
	posts := make([]domain.Post, 0, len(records))
	seen := map[string]bool{}
	for _, t := range records {
		if t.Legacy.AuthorID != c.owner.ID || t.Legacy.ReplyTo != "" || seen[t.ID] {
			continue
		}
		p, err := t.post()
		if err != nil {
			return nil, err
		}
		seen[t.ID] = true
		posts = append(posts, p)
		if len(posts) == 20 {
			break
		}
	}
	if len(posts) == 0 {
		return nil, fmt.Errorf("own-post timeline has no verified posts; empty state has not been established")
	}
	return posts, nil
}

type Image struct {
	Path   string `json:"path"`
	MIME   string `json:"mime_type"`
	Size   int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	data   []byte
}

func LoadImage(path string) (*Image, error) {
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
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 5<<20 {
		return nil, fmt.Errorf("image %q must be a regular file of 1–5242880 bytes", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, (5<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 5<<20 {
		return nil, fmt.Errorf("image %q exceeds 5 MiB", path)
	}
	mime := http.DetectContentType(data)
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" {
		return nil, fmt.Errorf("image %q has unsupported type %q; use PNG, JPEG or WebP", path, mime)
	}
	return &Image{Path: path, MIME: mime, Size: len(data), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), data: data}, nil
}

func (c *Client) uploadImage(ctx context.Context, image *Image) (string, error) {
	base := "https://upload.x.com/i/media/upload.json"
	q := url.Values{"command": {"INIT"}, "total_bytes": {strconv.Itoa(image.Size)}, "media_type": {image.MIME}, "media_category": {"tweet_image"}}
	data, err := c.request(ctx, http.MethodPost, base+"?"+q.Encode(), "", nil)
	if err != nil {
		return "", err
	}
	var init struct {
		ID string `json:"media_id_string"`
	}
	if err := json.Unmarshal(data, &init); err != nil || init.ID == "" {
		return "", fmt.Errorf("image INIT returned no media_id_string")
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("media", "blob")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(image.data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	q = url.Values{"command": {"APPEND"}, "media_id": {init.ID}, "segment_index": {"0"}}
	if _, err := c.request(ctx, http.MethodPost, base+"?"+q.Encode(), w.FormDataContentType(), body.Bytes()); err != nil {
		return "", err
	}
	q = url.Values{"command": {"FINALIZE"}, "media_id": {init.ID}, "original_md5": {fmt.Sprintf("%x", md5.Sum(image.data))}}
	data, err = c.request(ctx, http.MethodPost, base+"?"+q.Encode(), "", nil)
	if err != nil {
		return "", err
	}
	var final struct {
		ID         string          `json:"media_id_string"`
		Processing json.RawMessage `json:"processing_info"`
		Image      struct {
			Width  int `json:"w"`
			Height int `json:"h"`
		} `json:"image"`
	}
	if err := json.Unmarshal(data, &final); err != nil || final.ID != init.ID || final.Image.Width < 1 || final.Image.Height < 1 {
		return "", fmt.Errorf("image FINALIZE did not confirm the uploaded media")
	}
	if len(final.Processing) > 0 && string(final.Processing) != "null" {
		return "", fmt.Errorf("image is still processing; this CLI does not poll or publish incomplete media")
	}
	return init.ID, nil
}

// CreatePost is called only after the CLI has recorded the reviewed package.
// The result is accepted only after an independent read of the exact post.
func (c *Client) CreatePost(ctx context.Context, draft domain.Draft, image *Image, replyTo string, receiptWriter io.Writer) (domain.Post, error) {
	if c.owner.ID == "" || c.operations[domain.CreateOperation().String()] == "" {
		return domain.Post{}, fmt.Errorf("publication requires a verified signed-in account and create operation")
	}
	media := []map[string]any{}
	var mediaID string
	if image != nil {
		id, err := c.uploadImage(ctx, image)
		if err != nil {
			return domain.Post{}, err
		}
		mediaID = id
		media = append(media, map[string]any{"media_id": id, "tagged_users": []string{}})
	}
	variables := map[string]any{"tweet_text": draft.Text(), "media": map[string]any{"media_entities": media, "possibly_sensitive": false}, "semantic_annotation_ids": []string{}, "disallowed_reply_options": nil, "semantic_annotation_options": map[string]any{"composition_signal_1": true, "source": "Profile"}}
	if replyTo != "" {
		variables["reply"] = map[string]any{"in_reply_to_tweet_id": replyTo, "exclude_reply_user_ids": []string{}}
		variables["semantic_annotation_options"] = map[string]any{"composition_signal_1": true, "source": "Unknown"}
	}
	id := c.operations["CreateTweet"]
	body, err := json.Marshal(map[string]any{"variables": variables, "features": features, "queryId": id})
	if err != nil {
		return domain.Post{}, err
	}
	referer := Origin + "/" + c.owner.Handle
	if replyTo != "" {
		referer += "/status/" + replyTo
	}
	data, err := c.request(ctx, http.MethodPost, Origin+"/i/api/graphql/"+id+"/CreateTweet", "application/json", body, http.Header{"Referer": {referer}})
	if receiptWriter != nil {
		if _, err := receiptWriter.Write(data); err != nil {
			return domain.Post{}, fmt.Errorf("save create receipt after transmission: %w", err)
		}
	}
	if err != nil {
		return domain.Post{}, err
	}
	if err := graphQLError(data); err != nil {
		return domain.Post{}, err
	}
	var receipt struct {
		Data struct {
			Create struct {
				Results struct {
					Result tweetRecord `json:"result"`
				} `json:"tweet_results"`
			} `json:"create_tweet"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		return domain.Post{}, fmt.Errorf("decode create receipt: %w", err)
	}
	created := receipt.Data.Create.Results.Result
	if (created.Core.User.Result.ID != "" && created.Core.User.Result.ID != c.owner.ID) || (created.Legacy.AuthorID != "" && created.Legacy.AuthorID != c.owner.ID) {
		return domain.Post{}, fmt.Errorf("create receipt belongs to a different author")
	}
	if created.ID == "" {
		created.ID = created.Legacy.ID
	}
	if created.Legacy.ID != "" && created.Legacy.ID != created.ID {
		return domain.Post{}, fmt.Errorf("create receipt contains conflicting post identifiers")
	}
	ref, err := domain.NewPostRef(created.ID)
	if err != nil {
		return domain.Post{}, err
	}
	readback, err := c.ReadPost(ctx, ref)
	if err != nil {
		return domain.Post{}, fmt.Errorf("post may be published at %s/%s/status/%s; readback failed: %w", Origin, c.owner.Handle, created.ID, err)
	}
	if readback.Author.ID != c.owner.ID || strings.TrimSpace(readback.Text) != strings.TrimSpace(draft.Text()) || readback.ReplyTo != replyTo || len(readback.MediaIDs) != len(media) || (mediaID != "" && readback.MediaIDs[0] != mediaID) {
		return domain.Post{}, fmt.Errorf("post may be published at %s but author/text/media/reply readback did not match; inspect before retrying", readback.URL)
	}
	return readback, nil
}
