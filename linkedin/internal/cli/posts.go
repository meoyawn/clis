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
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"github.com/meoyawn/clis/linkedin/internal/service"
)

type postCreateOptions struct {
	publish        bool
	expectedAuthor string
	expectedSHA256 string
	recordDir      string
	image          *service.PostImage
}

type postCreateResult struct {
	Author         person             `json:"author"`
	Audience       string             `json:"audience"`
	Comments       string             `json:"comments"`
	Text           string             `json:"text"`
	SHA256         string             `json:"sha256"`
	Image          *service.PostImage `json:"image,omitempty"`
	Published      bool               `json:"published"`
	Verified       bool               `json:"verified"`
	AlreadyPresent bool               `json:"already_present"`
	PostURL        string             `json:"post_url,omitempty"`
	RecordPath     string             `json:"record_path,omitempty"`
}

type postAttempt struct {
	AuthorURL   string             `json:"author_url"`
	SHA256      string             `json:"sha256"`
	Text        string             `json:"text"`
	AttemptedAt string             `json:"attempted_at"`
	Published   bool               `json:"published"`
	PostURL     string             `json:"post_url,omitempty"`
	Image       *service.PostImage `json:"image,omitempty"`
	AssetID     string             `json:"asset_id,omitempty"`
}

func runPosts(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		return writeHelp(stdout, []string{"posts"})
	}
	command := args[0]
	if command == "edit" {
		return runPostEdit(ctx, args[1:], store, stdout, stderr)
	}
	if command == "comment" || command == "comments" {
		return runComments(ctx, args, store, stdout, stderr)
	}
	if command != "create" && command != "read" {
		return fmt.Errorf("unknown posts command %q; expected create, read, edit, comment, or comments", command)
	}
	fs := flag.NewFlagSet("posts "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _ = writeHelp(stderr, []string{"posts", command}) }
	var textFile, saveResponse, imagePath string
	opts := postCreateOptions{recordDir: filepath.Join(store.dir, "posts")}
	if command == "create" {
		fs.StringVar(&textFile, "text-file", "", "UTF-8 file containing the exact post")
		fs.StringVar(&imagePath, "image", "", "one native PNG, JPEG or WebP image")
		fs.BoolVar(&opts.publish, "publish", false, "publish the exact human-approved post; otherwise preview only")
		fs.StringVar(&opts.expectedAuthor, "expect-author", "", "reviewed author profile URL; required to publish")
		fs.StringVar(&opts.expectedSHA256, "expect-sha256", "", "reviewed text SHA-256 from preview; required to publish")
	} else {
		fs.StringVar(&saveResponse, "save-response", "", "save the raw post document to a new private file")
	}
	positionals, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(stdout, []string{"posts", command})
	}
	if err != nil {
		return err
	}
	var draft domain.PostDraft
	var ref domain.PostRef
	if command == "create" {
		if len(positionals) != 0 || textFile == "" {
			return fmt.Errorf("posts create requires --text-file and no positional arguments")
		}
		f, err := os.Open(textFile)
		if err != nil {
			return fmt.Errorf("open --text-file %q: %w", textFile, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(f, 12_001))
		f.Close()
		if readErr != nil {
			return fmt.Errorf("read --text-file %q: %w", textFile, readErr)
		}
		draft, err = domain.NewPostDraft(string(data))
		if err != nil {
			return err
		}
		opts.image, err = service.LoadPostImage(imagePath)
		if err != nil {
			return err
		}
		if opts.publish && (opts.expectedAuthor == "" || opts.expectedSHA256 == "") {
			return fmt.Errorf("--publish requires --expect-author and --expect-sha256 from the reviewed preview")
		}
	} else {
		if len(positionals) != 1 {
			return fmt.Errorf("posts read requires one exact feed permalink or activity URN; got %d arguments", len(positionals))
		}
	}
	client, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	if command == "read" {
		ref, err = client.resolvePost(ctx, positionals[0])
		if err != nil {
			return err
		}
		var capture io.Writer
		if saveResponse != "" {
			f, err := os.OpenFile(saveResponse, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return fmt.Errorf("create post capture %q: %w", saveResponse, err)
			}
			defer f.Close()
			capture = f
		}
		post, err := client.readPost(ctx, ref, capture)
		if err != nil {
			return err
		}
		return writeJSON(stdout, post)
	}
	result, err := client.createPost(ctx, draft, opts)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func (c *linkedinClient) readPost(ctx context.Context, ref domain.PostRef, capture io.Writer) (domain.Post, error) {
	data, err := c.readPostDocument(ctx, ref, capture)
	if err != nil {
		return domain.Post{}, err
	}
	return service.ParsePostDocument(ref, string(data))
}

func (c *linkedinClient) readPostDocument(ctx context.Context, ref domain.PostRef, capture io.Writer) ([]byte, error) {
	u, err := url.Parse(ref.URL())
	if err != nil {
		return nil, err
	}
	resp, err := c.get(ctx, u.RequestURI(), http.Header{"Accept": {"text/html"}, "Accept-Language": {"en-US,en;q=0.9"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read post document: %w", err)
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("post document exceeds 16 MiB")
	}
	if capture != nil {
		if _, err := capture.Write(data); err != nil {
			return nil, fmt.Errorf("save post document: %w", err)
		}
	}
	return data, nil
}

func postContentHash(draft domain.PostDraft, image *service.PostImage) string {
	if image == nil {
		return draft.SHA256()
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(draft.Text()+"\x00"+image.SHA256)))
}

func (c *linkedinClient) createPost(ctx context.Context, draft domain.PostDraft, opts postCreateOptions) (postCreateResult, error) {
	if _, err := domain.NewPostDraft(draft.Text()); err != nil {
		return postCreateResult{}, err
	}
	contentHash := postContentHash(draft, opts.image)
	if opts.publish && (opts.expectedAuthor == "" || opts.expectedSHA256 != contentHash) {
		return postCreateResult{}, fmt.Errorf("post text or author approval is missing or changed; preview and review the final package again")
	}
	self, err := c.mailbox(ctx)
	if err != nil {
		return postCreateResult{}, err
	}
	author, err := c.profile(ctx, profileRef{ID: strings.TrimPrefix(self, "urn:li:fsd_profile:")})
	if err != nil {
		return postCreateResult{}, err
	}
	if author.ProfileURN != self {
		return postCreateResult{}, fmt.Errorf("post author does not match the signed-in profile")
	}
	result := postCreateResult{Author: author.person, Audience: "Anyone", Comments: "Anyone", Text: draft.Text(), SHA256: contentHash, Image: opts.image}
	if !opts.publish {
		return result, nil
	}
	if author.URL != opts.expectedAuthor {
		return result, fmt.Errorf("signed-in post author %q does not match reviewed author %q", author.URL, opts.expectedAuthor)
	}
	if err := os.MkdirAll(opts.recordDir, 0700); err != nil {
		return result, fmt.Errorf("create post history: %w", err)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(author.URL+"\x00"+contentHash)))
	recordPath := filepath.Join(opts.recordDir, key+".json")
	result.RecordPath = recordPath
	record, err := os.OpenFile(recordPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		data, readErr := os.ReadFile(recordPath)
		var previous postAttempt
		if readErr != nil || json.Unmarshal(data, &previous) != nil || previous.AuthorURL != author.URL || previous.SHA256 != contentHash || previous.Text != draft.Text() {
			return result, fmt.Errorf("post history %q cannot be verified; no POST sent", recordPath)
		}
		if previous.Published && previous.PostURL != "" {
			result.Published, result.Verified, result.AlreadyPresent, result.PostURL = true, true, true, previous.PostURL
			return result, nil
		}
		return result, fmt.Errorf("an earlier publish attempt is unconfirmed; inspect your profile and %q before any retry; no POST sent", recordPath)
	}
	if err != nil {
		return result, fmt.Errorf("reserve post attempt: %w", err)
	}
	attempt := postAttempt{AuthorURL: author.URL, SHA256: contentHash, Text: draft.Text(), Image: opts.image, AttemptedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	writeErr := writeJSON(record, attempt)
	if writeErr == nil {
		writeErr = record.Sync()
	}
	closeErr := record.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return result, fmt.Errorf("save post attempt before publishing: %w", err)
	}
	var body []byte
	if opts.image != nil {
		attempt.AssetID, err = c.uploadPostImage(ctx, opts.image, recordPath)
		if err != nil {
			return result, fmt.Errorf("image preparation stopped; no CreatePost request sent; inspect %q: %w", recordPath, err)
		}
		if err := savePostAttempt(recordPath, attempt); err != nil {
			return result, err
		}
		body, err = service.PrepareImagePost(draft, attempt.AssetID)
	} else {
		body, err = service.PreparePost(draft)
	}
	if err != nil {
		return result, err
	}
	resp, err := c.request(ctx, http.MethodPost, service.CreatePostPath, bytes.NewReader(body), http.Header{
		"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"}, "Referer": {"https://www.linkedin.com/sharing/compose"},
	})
	if err != nil {
		return result, fmt.Errorf("publish outcome unconfirmed; inspect your profile and %q before retrying: %w", recordPath, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	resp.Body.Close()
	receiptPath := recordPath + ".receipt"
	if err := os.WriteFile(receiptPath, data, 0600); err != nil {
		return result, fmt.Errorf("publish response received but receipt could not be saved; inspect your profile before retrying: %w", err)
	}
	if readErr != nil || len(data) > 16<<20 {
		return result, fmt.Errorf("publish receipt incomplete; inspect %q and your profile before retrying", receiptPath)
	}
	ref, err := service.PostReceipt(data)
	if err != nil {
		return result, fmt.Errorf("publish outcome unconfirmed; inspect %q and your profile before retrying: %w", receiptPath, err)
	}
	result.PostURL = ref.URL()
	actual, err := c.readPost(ctx, ref, nil)
	if err != nil {
		return result, fmt.Errorf("post receipt refers to %q but readback failed; do not resend: %w", ref.URL(), err)
	}
	if actual.AuthorURL != author.URL || strings.TrimSpace(strings.ReplaceAll(actual.Text, "\r\n", "\n")) != strings.TrimSpace(strings.ReplaceAll(draft.Text(), "\r\n", "\n")) {
		return result, fmt.Errorf("post %q does not match reviewed author/text; do not resend", ref.URL())
	}
	if opts.image != nil && !slices.Contains(actual.ImageAssetIDs, attempt.AssetID) {
		return result, fmt.Errorf("post %q does not confirm the uploaded image asset; do not resend", ref.URL())
	}
	attempt.Published, attempt.PostURL = true, ref.URL()
	confirmed, err := os.CreateTemp(opts.recordDir, ".confirmed-*")
	if err != nil {
		return result, fmt.Errorf("post %q verified but history save failed; do not resend: %w", ref.URL(), err)
	}
	defer os.Remove(confirmed.Name())
	writeErr = writeJSON(confirmed, attempt)
	closeErr = confirmed.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return result, fmt.Errorf("post %q verified but history save failed; do not resend: %w", ref.URL(), err)
	}
	if err := os.Rename(confirmed.Name(), recordPath); err != nil {
		return result, fmt.Errorf("post %q verified but history save failed; do not resend: %w", ref.URL(), err)
	}
	result.Published, result.Verified = true, true
	return result, nil
}

func savePostAttempt(path string, attempt postAttempt) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".attempt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := writeJSON(f, attempt); err != nil {
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

func (c *linkedinClient) imageAction(ctx context.Context, action domain.ImageAction, body []byte, receiptPath string) ([]byte, error) {
	resp, err := c.request(ctx, http.MethodPost, "/flagship-web/rsc-action/actions/server-request?sduiid="+action.String(), bytes.NewReader(body), http.Header{"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"}, "Referer": {"https://www.linkedin.com/sharing/compose"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("image action receipt exceeds 16 MiB")
	}
	f, err := os.OpenFile(receiptPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *linkedinClient) uploadPostImage(ctx context.Context, image *service.PostImage, recordPath string) (string, error) {
	plan, err := service.NewImageUpload(image)
	if err != nil {
		return "", err
	}
	body, err := plan.PrepareUpload()
	if err != nil {
		return "", err
	}
	data, err := c.imageAction(ctx, domain.ImageUploadAction(), body, recordPath+".image-init.receipt")
	if err != nil {
		return "", err
	}
	instruction, err := service.ParseImageUpload(data)
	if err != nil {
		return "", err
	}
	instruction.Headers.Set("Content-Type", image.MIME)
	resp, err := c.request(ctx, http.MethodPut, instruction.Path, image.Reader(), instruction.Headers)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	body, err = plan.PrepareRegistration(instruction.AssetID)
	if err != nil {
		return "", err
	}
	data, err = c.imageAction(ctx, domain.ImageRegisterAction(), body, recordPath+".image-register.receipt")
	if err != nil {
		return "", err
	}
	if err := service.ConfirmImageRegistration(data, instruction.AssetID); err != nil {
		return "", err
	}
	return instruction.AssetID, nil
}

const postsHelp = `Preview and publish approved personal LinkedIn feed posts.

Usage:
  linkedin posts create --text-file FILE [--image FILE] [--publish --expect-author URL --expect-sha256 HASH]
  linkedin posts read <feed-permalink-or-activity-URN>
  linkedin posts edit <post> --text-file FILE [--apply --expect-author URL --expect-sha256 HASH --expect-current-sha256 HASH]
  linkedin posts comment <post> --text-file FILE [--publish --expect-author URL --expect-sha256 HASH]
  linkedin posts comments <post>

Create previews the signed-in author, public audience (Anyone), comments (Anyone),
exact UTF-8 text, optional image metadata and SHA-256. It makes only two identity GETs and writes nothing.
Text is limited to 3000 UTF-16 code units and is sent unchanged, including links.
One PNG/JPEG/WebP image up to 5 MiB is supported. The preview hash covers text and
image bytes. Preview uploads nothing; publishing initializes, uploads and registers
the image through the observed API, then creates the post. No link cards.

Show the full package in chat and obtain explicit approval before --publish.
The two --expect flags bind publishing to the reviewed identity and text; they
do not replace human approval. Publishing makes one CreatePost POST, saves its
receipt privately, and reads the exact activity permalink to verify author/text.
No retries. Repeating a confirmed post is a local no-op; an unconfirmed attempt
blocks another POST. Inspect the saved receipt and your profile before retrying.
History prevents duplicates made by this CLI/config directory, not other clients.
Read is one GET, with no reactions, read receipts, comments or other mutations.
Read and edit also resolve https://lnkd.in/p/ links without sending account
cookies to the shortener; share permalinks are read to identify the activity.

Edit previews the exact signed-in author, current post, original images,
replacement text and both text hashes. It reads the native post menu and edit
form without saving changes. --apply requires an authorized edit of that target
and all three expectation flags. It preserves the existing media, journals the
attempt privately, follows only native update actions bound to the target/text,
then reads the original activity to verify author, commentary and image assets.
Concurrent text changes stop the update. Unconfirmed attempts are never retried;
an already matching post is a verified no-op. No new post or image is created.

Comment previews require your own parent post and print the exact author, target,
text and package SHA-256. Comments are limited to 1250 UTF-16 code units. Publishing
saves a private attempt before one comment POST and reads the parent again to
verify the new comment's ID, author, text and parent. A matching existing comment
is a verified no-op, including reconciliation after an uncertain local receipt.
An uncertain attempt without a matching readback blocks another POST.
comments reads the rendered first page, preserving expanded external link targets;
it does not claim to exhaust older comments. No image, reply or link-preview cards.
`
