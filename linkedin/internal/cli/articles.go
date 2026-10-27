package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const articlesHelp = `Usage:
  go run . articles read-draft ID|https://www.linkedin.com/article/edit/ID/
  go run . articles delete-draft ID|URL
  go run . articles delete-draft ID|URL --delete --expect-author PROFILE_URL --expect-sha256 HASH

Read and preview deletion of one exact personal article draft. A delete requires
the signed-in author, DRAFT state, no publication metadata and the exact preview
hash. Saves a private copy before one DELETE, then independently reads the draft
finder to confirm absence. Published and scheduled articles are refused.
No retries. An uncertain attempt blocks another deletion while the draft exists.
`

type articleDraft struct {
	URN         string `json:"entityUrn"`
	Title       string `json:"title,omitempty"`
	ContentHTML string `json:"contentHtml"`
	State       string `json:"state"`
	Version     int    `json:"version"`
	UpdatedAt   int64  `json:"updatedAt"`
	Authors     []struct {
		ProfileURN string `json:"profileUrn"`
	} `json:"authors"`
	PublishedAt *int64 `json:"publishedAt,omitempty"`
	ScheduledAt *int64 `json:"scheduledAt,omitempty"`
	ActivityURN string `json:"activityUrn,omitempty"`
	UGCPostURN  string `json:"ugcPostUrn,omitempty"`
}
type articleOptions struct {
	delete                  bool
	author, hash, recordDir string
}
type articleResult struct {
	AuthorURL  string        `json:"author_url"`
	URL        string        `json:"url"`
	Draft      *articleDraft `json:"draft,omitempty"`
	SHA256     string        `json:"sha256,omitempty"`
	Exists     bool          `json:"exists"`
	Deleted    bool          `json:"deleted"`
	Verified   bool          `json:"verified"`
	RecordPath string        `json:"record_path,omitempty"`
}

var articleIDPattern = regexp.MustCompile(`^[1-9][0-9]{1,24}$`)

func parseArticleID(value string) (string, error) {
	if articleIDPattern.MatchString(value) {
		return value, nil
	}
	u, err := url.Parse(value)
	if err == nil && u.Scheme == "https" && u.Host == "www.linkedin.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 3 && parts[0] == "article" && parts[1] == "edit" && articleIDPattern.MatchString(parts[2]) {
			return parts[2], nil
		}
	}
	return "", fmt.Errorf("invalid article draft %q; use its numeric ID or exact /article/edit/ID/ URL", value)
}

func runArticles(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		_, err := io.WriteString(out, articlesHelp)
		return err
	}
	if args[0] != "read-draft" && args[0] != "delete-draft" {
		return fmt.Errorf("articles requires read-draft or delete-draft")
	}
	fs := flag.NewFlagSet("articles "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	opts := articleOptions{recordDir: filepath.Join(store.dir, "article-deletions")}
	if args[0] == "delete-draft" {
		fs.BoolVar(&opts.delete, "delete", false, "delete the reviewed exact draft")
		fs.StringVar(&opts.author, "expect-author", "", "approved profile URL")
		fs.StringVar(&opts.hash, "expect-sha256", "", "preview content hash")
	}
	positionals, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(out, articlesHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("articles %s requires one exact draft", args[0])
	}
	id, err := parseArticleID(positionals[0])
	if err != nil {
		return err
	}
	if opts.delete && (opts.author == "" || opts.hash == "") {
		return fmt.Errorf("--delete requires --expect-author and --expect-sha256")
	}
	c, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	result, err := c.articleDraftAction(ctx, id, opts)
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

func (c *linkedinClient) readArticleDraft(ctx context.Context, id string) (*articleDraft, error) {
	q := url.Values{"q": {"latestDraftById"}, "firstPartyArticleUrn": {"urn:li:fsd_firstPartyArticle:" + id}}
	resp, err := c.get(ctx, "/voyager/api/voyagerPublishingDashFirstPartyArticles?"+q.Encode(), http.Header{"Accept": {"application/json"}})
	if err != nil {
		var failure *diagnosticError
		if errors.As(err, &failure) && failure.status == http.StatusNotFound && failure.restliError {
			return nil, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("article response exceeds 4 MiB")
	}
	var response struct {
		Elements *[]articleDraft   `json:"elements"`
		Errors   []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	if response.Elements == nil || len(response.Errors) != 0 {
		return nil, fmt.Errorf("article finder did not return an error-free collection")
	}
	if len(*response.Elements) == 0 {
		return nil, nil
	}
	if len(*response.Elements) != 1 || (*response.Elements)[0].URN != "urn:li:fsd_firstPartyArticle:"+id {
		return nil, fmt.Errorf("article finder returned a different or ambiguous draft")
	}
	return &(*response.Elements)[0], nil
}

func (c *linkedinClient) articleDraftAction(ctx context.Context, id string, opts articleOptions) (articleResult, error) {
	if !articleIDPattern.MatchString(id) {
		return articleResult{}, fmt.Errorf("invalid article ID %q", id)
	}
	self, err := c.mailbox(ctx)
	if err != nil {
		return articleResult{}, err
	}
	author, err := c.profile(ctx, profileRef{ID: strings.TrimPrefix(self, "urn:li:fsd_profile:")})
	if err != nil {
		return articleResult{}, err
	}
	if author.ProfileURN != self {
		return articleResult{}, fmt.Errorf("article owner does not match signed-in profile")
	}
	result := articleResult{AuthorURL: author.URL, URL: "https://www.linkedin.com/article/edit/" + id + "/"}
	draft, err := c.readArticleDraft(ctx, id)
	if err != nil {
		return result, err
	}
	if draft == nil {
		result.Deleted, result.Verified = true, true
		return result, nil
	}
	result.Draft, result.Exists = draft, true
	if draft.State != "DRAFT" || draft.PublishedAt != nil || draft.ScheduledAt != nil || draft.ActivityURN != "" || draft.UGCPostURN != "" {
		return result, fmt.Errorf("article %q is not an unpublished, unscheduled draft; deletion refused", id)
	}
	if len(draft.Authors) != 1 || draft.Authors[0].ProfileURN != self {
		return result, fmt.Errorf("article %q does not belong exclusively to the signed-in author", id)
	}
	data, err := json.Marshal(draft)
	if err != nil {
		return result, err
	}
	result.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	if !opts.delete {
		return result, nil
	}
	if opts.author != author.URL || opts.hash != result.SHA256 {
		return result, fmt.Errorf("article draft or author changed since preview; no DELETE sent")
	}
	if err := os.MkdirAll(opts.recordDir, 0700); err != nil {
		return result, err
	}
	path := filepath.Join(opts.recordDir, id+"-"+result.SHA256+".json")
	result.RecordPath = path
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, fmt.Errorf("reserve article deletion; inspect existing record before retrying: %w", err)
	}
	err = writeJSON(f, map[string]any{"draft": draft, "author_url": author.URL, "sha256": result.SHA256, "attempted_at": time.Now().UTC(), "deleted": false})
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return result, err
	}
	resp, err := c.request(ctx, http.MethodDelete, "/voyager/api/voyagerPublishingDashFirstPartyArticles/"+draft.URN, nil, nil)
	if err != nil {
		return result, fmt.Errorf("article deletion unconfirmed; inspect %q and read-draft before retrying: %w", path, err)
	}
	resp.Body.Close()
	remaining, err := c.readArticleDraft(ctx, id)
	if err != nil {
		return result, fmt.Errorf("DELETE sent but independent draft read failed; do not resend: %w", err)
	}
	if remaining != nil {
		return result, fmt.Errorf("DELETE returned but exact draft still exists; do not resend; inspect %q", path)
	}
	result.Exists, result.Deleted, result.Verified = false, true, true
	confirmed, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(path+".verified.json", append(confirmed, '\n'), 0600); err != nil {
		return result, fmt.Errorf("article absence verified but local confirmation could not be saved; do not resend: %w", err)
	}
	return result, nil
}
