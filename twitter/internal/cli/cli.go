package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service"
)

const help = `Use your existing X session through SweetCookie. Run from this module.

Usage:
  go run . auth import [--profile 'Default']
  go run . auth status
  go run . me
  go run . posts list
  go run . posts read [--save-response NEW.json] ID|https://x.com/HANDLE/status/ID
  go run . posts replies [--save-response NEW.json] ID|https://x.com/HANDLE/status/ID
  go run . posts create --text-file FILE [--image FILE] [--reply-to ID|URL]
  go run . posts create --text-file FILE [--image FILE] [--reply-to ID|URL] 
    --publish --expect-author HANDLE --expect-sha256 PACKAGE_HASH

Create previews by default, including the exact author, text, image hash and
reply target. It never uploads in preview. Review that package before using
--publish with its package_sha256. Replies are supported only to your own posts.
One static PNG/JPEG/WebP image, at most 5 MiB; no video, polls or long posts.
Text is never truncated. A conservative 280-character upper bound may overcount
URLs and compound emoji. A saved attempt blocks duplicate/uncertain sends.

auth import/status are local-only. Other commands read the current X bootstrap
and public app bundle, then verify the selected account. Reads export no private
contact fields. posts list is one page of up to 20 own original posts, with
available views and engagement counters; no pagination or attribution claims.
HTTP 429, auth errors, redirects and API restrictions stop without retries.
TWITTER_CONFIG_DIR overrides the OS user configuration directory/twitter.
`

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			_, err := fmt.Fprint(out, help)
			return err
		}
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := fmt.Fprint(out, help)
		return err
	}
	dir := os.Getenv("TWITTER_CONFIG_DIR")
	if dir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(root, "twitter")
	}
	if args[0] == "auth" {
		return auth(ctx, args[1:], dir, out, errOut)
	}
	if args[0] == "posts" && len(args) > 1 && args[1] == "create" {
		return create(ctx, args[2:], dir, out, errOut)
	}
	if (args[0] != "me" || len(args) != 1) && (args[0] != "posts" || len(args) < 2 || (args[1] != "read" && args[1] != "list" && args[1] != "replies")) {
		return fmt.Errorf("unknown command %q; run go run . --help", strings.Join(args, " "))
	}
	var ref domain.PostRef
	var capturePath string
	if args[0] == "posts" {
		if args[1] == "read" || args[1] == "replies" {
			fs := flag.NewFlagSet("posts "+args[1], flag.ContinueOnError)
			fs.SetOutput(errOut)
			fs.StringVar(&capturePath, "save-response", "", "save raw private JSON to a new 0600 file")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 1 {
				return fmt.Errorf("posts %s requires one ID or URL", args[1])
			}
			var err error
			ref, err = domain.NewPostRef(fs.Arg(0))
			if err != nil {
				return err
			}
		} else if len(args) != 2 {
			return fmt.Errorf("posts list takes no arguments")
		}
	}
	c, err := loadClient(dir)
	if err != nil {
		return err
	}
	owner, err := c.Bootstrap(ctx)
	if err != nil {
		return err
	}
	if args[0] == "me" {
		return writeJSON(out, map[string]any{"profile": owner, "verified": true, "fetched_at": time.Now().UTC()})
	}
	if capturePath != "" {
		f, err := os.OpenFile(capturePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("create private response capture: %w", err)
		}
		defer f.Close()
		c.CapturePostResponses(f)
	}
	if args[1] == "read" {
		post, err := c.ReadPost(ctx, ref)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"post": post, "fetched_at": time.Now().UTC()})
	}
	if args[1] == "replies" {
		posts, err := c.OwnReplies(ctx, ref)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"owner": owner, "parent_id": ref.ID(), "replies": posts, "coverage": "direct own replies checked in the conversation and first own-replies page; history not exhausted", "fetched_at": time.Now().UTC()})
	}
	posts, err := c.OwnPosts(ctx)
	if err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"owner": owner, "posts": posts, "coverage": "first page, up to 20 original posts; replies/history not exhausted", "fetched_at": time.Now().UTC()})
}

func loadClient(dir string) (*service.Client, error) {
	s, err := service.LoadSession(dir)
	if err != nil {
		return nil, err
	}
	return service.NewClient(service.HTTPClient(), s), nil
}

func auth(ctx context.Context, args []string, dir string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("auth requires import or status")
	}
	if args[0] == "import" {
		fs := flag.NewFlagSet("auth import", flag.ContinueOnError)
		fs.SetOutput(errOut)
		profile := fs.String("profile", "Default", "Chrome profile name or directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("auth import takes flags only")
		}
		s, err := service.ImportSession(ctx, *profile)
		if err != nil {
			return err
		}
		if err := service.SaveSession(dir, s); err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"imported": true, "source": "chrome", "profile": s.Profile, "imported_at": s.ImportedAt, "expires_at": s.ExpiresAt, "network_verified": false})
	}
	if args[0] != "status" || len(args) != 1 {
		return fmt.Errorf("unsupported auth command %q; use import or status", args[0])
	}
	s, err := service.LoadSession(dir)
	return writeJSON(out, map[string]any{"usable": err == nil, "profile": s.Profile, "imported_at": s.ImportedAt, "expires_at": s.ExpiresAt, "network_verified": false, "reason": errorText(err)})
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type packagePreview struct {
	Author           domain.Profile `json:"author"`
	Text             string         `json:"text"`
	Image            *service.Image `json:"image"`
	ReplyTo          *domain.Post   `json:"reply_to"`
	LengthUpperBound int            `json:"length_upper_bound"`
	SHA256           string         `json:"package_sha256"`
	Published        bool           `json:"published"`
	Verified         bool           `json:"verified"`
	AlreadyPresent   bool           `json:"already_present"`
	Post             *domain.Post   `json:"post,omitempty"`
	AttemptPath      string         `json:"attempt_path,omitempty"`
}

func create(ctx context.Context, args []string, dir string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("posts create", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("text-file", "", "UTF-8 plain text file")
	imagePath := fs.String("image", "", "one PNG, JPEG or WebP")
	reply := fs.String("reply-to", "", "own post ID or URL")
	publish := fs.Bool("publish", false, "publish the reviewed package")
	expectAuthor := fs.String("expect-author", "", "reviewed author handle")
	expectHash := fs.String("expect-sha256", "", "reviewed package_sha256")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *file == "" {
		return fmt.Errorf("posts create requires --text-file FILE and no positional arguments")
	}
	text, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("read --text-file %q: %w", *file, err)
	}
	draft, err := domain.NewDraft(string(text))
	if err != nil {
		return err
	}
	image, err := service.LoadImage(*imagePath)
	if err != nil {
		return err
	}
	var ref domain.PostRef
	if *reply != "" {
		ref, err = domain.NewPostRef(*reply)
		if err != nil {
			return err
		}
	}
	if *publish && (*expectAuthor == "" || *expectHash == "") {
		return fmt.Errorf("--publish requires the reviewed --expect-author and --expect-sha256")
	}
	c, err := loadClient(dir)
	if err != nil {
		return err
	}
	if _, err := c.Bootstrap(ctx); err != nil {
		return err
	}
	var parent *domain.Post
	if *reply != "" {
		p, err := c.ReadPost(ctx, ref)
		if err != nil {
			return err
		}
		if p.Author.ID != c.Owner().ID {
			return fmt.Errorf("--reply-to %q belongs to another account; this CLI supports own-post replies only", *reply)
		}
		parent = &p
	}
	result := preview(c.Owner(), draft, image, parent)
	if !*publish {
		return writeJSON(out, result)
	}
	if !strings.EqualFold(strings.TrimPrefix(*expectAuthor, "@"), result.Author.Handle) || *expectHash != result.SHA256 {
		return fmt.Errorf("author or package changed; preview again before publishing")
	}
	return publishPackage(ctx, c, draft, image, parent, dir, result, out)
}

func preview(owner domain.Profile, draft domain.Draft, image *service.Image, parent *domain.Post) packagePreview {
	imageHash, replyID := "", ""
	if image != nil {
		imageHash = image.SHA256
	}
	if parent != nil {
		replyID = parent.ID
	}
	reviewed, _ := json.Marshal([]string{owner.ID, owner.Handle, draft.Text(), imageHash, replyID})
	return packagePreview{Author: owner, Text: draft.Text(), Image: image, ReplyTo: parent, LengthUpperBound: draft.LengthUpperBound(), SHA256: fmt.Sprintf("%x", sha256.Sum256(reviewed))}
}

func publishPackage(ctx context.Context, c *service.Client, draft domain.Draft, image *service.Image, parent *domain.Post, dir string, result packagePreview, out io.Writer) error {
	history := filepath.Join(dir, "posts")
	if err := os.MkdirAll(history, 0700); err != nil {
		return err
	}
	path := filepath.Join(history, result.SHA256+".json")
	result.AttemptPath = path
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var previous packagePreview
		if err := json.Unmarshal(data, &previous); err != nil {
			return fmt.Errorf("existing attempt record is invalid; inspect %q before retrying", path)
		}
		if previous.SHA256 == result.SHA256 && previous.Author.ID == result.Author.ID && previous.Text == result.Text && previous.Published && previous.Verified && previous.Post != nil {
			previous.AlreadyPresent = true
			return writeJSON(out, previous)
		}
		return fmt.Errorf("an earlier attempt is unconfirmed; inspect your profile and private record %q before retrying; no POST made", path)
	}
	if err != nil {
		return err
	}
	writeErr := writeJSON(f, result)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	replyID := ""
	if parent != nil {
		replyID = parent.ID
	}
	receipt, err := os.OpenFile(path+".receipt.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create private receipt file before publication: %w", err)
	}
	defer receipt.Close()
	post, err := c.CreatePost(ctx, draft, image, replyID, receipt)
	if err != nil {
		return fmt.Errorf("publication unconfirmed; do not retry automatically; attempt %q: %w", path, err)
	}
	result.Published = true
	result.Verified = true
	result.Post = &post
	tmp, err := os.CreateTemp(history, ".confirmed-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := writeJSON(tmp, result); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return writeJSON(out, result)
}

func writeJSON(out io.Writer, value any) error {
	e := json.NewEncoder(out)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	return e.Encode(value)
}
