package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"github.com/meoyawn/clis/linkedin/internal/service"
)

type commentOptions struct {
	publish                                 bool
	expectedAuthor, expectedHash, recordDir string
}
type commentResult struct {
	AuthorURL      string          `json:"author_url"`
	ParentURL      string          `json:"parent_url"`
	Text           string          `json:"text"`
	SHA256         string          `json:"sha256"`
	Published      bool            `json:"published"`
	Verified       bool            `json:"verified"`
	AlreadyPresent bool            `json:"already_present"`
	Comment        *domain.Comment `json:"comment,omitempty"`
	RecordPath     string          `json:"record_path,omitempty"`
}
type commentAttempt struct {
	Package     commentResult `json:"package"`
	AttemptedAt time.Time     `json:"attempted_at"`
}

func runComments(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("posts "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var file string
	opts := commentOptions{recordDir: filepath.Join(store.dir, "comments")}
	if args[0] == "comment" {
		fs.StringVar(&file, "text-file", "", "exact UTF-8 comment")
		fs.BoolVar(&opts.publish, "publish", false, "publish approved comment")
		fs.StringVar(&opts.expectedAuthor, "expect-author", "", "approved author URL")
		fs.StringVar(&opts.expectedHash, "expect-sha256", "", "approved package hash")
	}
	positionals, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(stdout, []string{"posts"})
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("posts %s requires one exact post URL or activity URN", args[0])
	}
	ref, err := domain.NewPostRef(positionals[0])
	if err != nil {
		return err
	}
	var draft domain.PostDraft
	if args[0] == "comment" {
		if file == "" {
			return fmt.Errorf("posts comment requires --text-file")
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, 12001))
		f.Close()
		if err != nil {
			return err
		}
		draft, err = domain.NewPostDraft(string(data))
		if err != nil {
			return err
		}
		if _, err := service.PrepareComment(ref, draft); err != nil {
			return err
		}
		if opts.publish && (opts.expectedAuthor == "" || opts.expectedHash == "") {
			return fmt.Errorf("--publish requires --expect-author and --expect-sha256")
		}
	}
	c, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	if args[0] == "comments" {
		data, err := c.readPostDocument(ctx, ref, nil)
		if err != nil {
			return err
		}
		comments, err := service.ParsePostComments(ref, string(data))
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"parent_url": ref.URL(), "comments": comments, "coverage": "rendered first page; history not exhausted", "fetched_at": time.Now().UTC()})
	}
	result, err := c.createComment(ctx, ref, draft, opts)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func (c *linkedinClient) createComment(ctx context.Context, ref domain.PostRef, draft domain.PostDraft, opts commentOptions) (commentResult, error) {
	body, err := service.PrepareComment(ref, draft)
	if err != nil {
		return commentResult{}, err
	}
	self, err := c.mailbox(ctx)
	if err != nil {
		return commentResult{}, err
	}
	author, err := c.profile(ctx, profileRef{ID: strings.TrimPrefix(self, "urn:li:fsd_profile:")})
	if err != nil {
		return commentResult{}, err
	}
	if author.ProfileURN != self {
		return commentResult{}, fmt.Errorf("comment author does not match signed-in account")
	}
	page, err := c.readPostDocument(ctx, ref, nil)
	if err != nil {
		return commentResult{}, err
	}
	parent, err := service.ParsePostDocument(ref, string(page))
	if err != nil {
		return commentResult{}, err
	}
	if parent.AuthorURL != author.URL {
		return commentResult{}, fmt.Errorf("comments are supported only on your own posts; parent author is %q", parent.AuthorURL)
	}
	comments, err := service.ParsePostComments(ref, string(page))
	if err != nil {
		return commentResult{}, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(author.URL+"\x00"+ref.URN()+"\x00"+draft.Text())))
	result := commentResult{AuthorURL: author.URL, ParentURL: ref.URL(), Text: draft.Text(), SHA256: hash}
	if !opts.publish {
		return result, nil
	}
	if opts.expectedAuthor != author.URL || opts.expectedHash != hash {
		return result, fmt.Errorf("comment author, text or parent changed; preview the approved package again")
	}
	if err := os.MkdirAll(opts.recordDir, 0700); err != nil {
		return result, err
	}
	path := filepath.Join(opts.recordDir, hash+".json")
	result.RecordPath = path
	for _, comment := range comments {
		if comment.AuthorURL == author.URL && strings.TrimSpace(comment.Text) == strings.TrimSpace(draft.Text()) {
			result.Comment = &comment
			result.Published, result.Verified, result.AlreadyPresent = true, true, true
			if data, err := os.ReadFile(path); err == nil {
				var previous commentAttempt
				if json.Unmarshal(data, &previous) != nil || previous.Package.SHA256 != hash || previous.Package.AuthorURL != author.URL || previous.Package.ParentURL != ref.URL() || previous.Package.Text != draft.Text() {
					return result, fmt.Errorf("existing comment history %q does not match the independently read package", path)
				}
				previous.Package = result
				if err := saveCommentAttempt(path, previous); err != nil {
					return result, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return result, err
			}
			return result, nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		// Always read the actual destination above before allowing reconciliation.
		return result, fmt.Errorf("earlier comment attempt exists in %q and no matching comment was read; no POST sent", path)
	}
	if err != nil {
		return result, err
	}
	attempt := commentAttempt{Package: result, AttemptedAt: time.Now().UTC()}
	err = writeJSON(f, attempt)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return result, err
	}
	receipt, err := os.OpenFile(path+".receipt", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	defer receipt.Close()
	resp, err := c.request(ctx, http.MethodPost, service.CreateCommentPath, bytes.NewReader(body), http.Header{"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"}, "Referer": {ref.URL()}})
	if err != nil {
		return result, fmt.Errorf("comment outcome unconfirmed; inspect %q before retrying: %w", path, err)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	resp.Body.Close()
	if _, writeErr := receipt.Write(data); writeErr != nil {
		return result, writeErr
	}
	if err != nil || len(data) > 16<<20 {
		return result, fmt.Errorf("comment receipt incomplete; do not resend; inspect %q", path)
	}
	if err := service.ConfirmCommentCompletion(data); err != nil {
		return result, fmt.Errorf("comment completion unconfirmed; inspect %q; do not resend: %w", path, err)
	}
	page, err = c.readPostDocument(ctx, ref, nil)
	if err != nil {
		return result, fmt.Errorf("comment transmitted but independent read failed; do not resend: %w", err)
	}
	comments, err = service.ParsePostComments(ref, string(page))
	if err != nil {
		return result, err
	}
	for _, comment := range comments {
		if comment.AuthorURL == author.URL && strings.TrimSpace(comment.Text) == strings.TrimSpace(draft.Text()) {
			if result.Comment != nil {
				return result, fmt.Errorf("multiple matching comments; inspect parent before continuing")
			}
			copy := comment
			result.Comment = &copy
		}
	}
	if result.Comment == nil {
		return result, fmt.Errorf("comment was transmitted but no exact author/text/parent match was read; inspect %q; do not resend", path)
	}
	result.Published, result.Verified = true, true
	attempt.Package = result
	if err := saveCommentAttempt(path, attempt); err != nil {
		return result, err
	}
	return result, nil
}

func saveCommentAttempt(path string, attempt commentAttempt) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".comment-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(attempt); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
