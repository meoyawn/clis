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

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service"
)

type deletionPreview struct {
	Author      domain.Profile `json:"author"`
	Post        domain.Post    `json:"post"`
	SHA256      string         `json:"package_sha256"`
	Deleted     bool           `json:"deleted"`
	Verified    bool           `json:"verified"`
	AttemptPath string         `json:"attempt_path,omitempty"`
}

func deletionFor(owner domain.Profile, post domain.Post) deletionPreview {
	data, _ := json.Marshal([]any{"delete", owner.ID, owner.Handle, post.ID, post.Text, post.MediaIDs, post.ReplyTo})
	return deletionPreview{Author: owner, Post: post, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
}

func deleteCommand(ctx context.Context, args []string, dir string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("posts delete", flag.ContinueOnError)
	fs.SetOutput(errOut)
	apply := fs.Bool("apply", false, "delete the reviewed own post")
	expectAuthor := fs.String("expect-author", "", "reviewed author handle")
	expectHash := fs.String("expect-sha256", "", "reviewed package_sha256")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("posts delete requires flags followed by one post ID or URL")
	}
	ref, err := domain.NewPostRef(fs.Arg(0))
	if err != nil {
		return err
	}
	if *apply && (*expectAuthor == "" || *expectHash == "") {
		return fmt.Errorf("--apply requires --expect-author and --expect-sha256")
	}
	c, err := loadClient(dir)
	if err != nil {
		return err
	}
	owner, err := c.Bootstrap(ctx)
	if err != nil {
		return err
	}
	if *apply && !strings.EqualFold(strings.TrimPrefix(*expectAuthor, "@"), owner.Handle) {
		return fmt.Errorf("selected author changed before deleting post %q", ref.ID())
	}
	path := filepath.Join(dir, "deletions", ref.ID()+".json")
	if *apply {
		data, err := os.ReadFile(path)
		if err == nil {
			var previous deletionPreview
			if err := json.Unmarshal(data, &previous); err != nil {
				return fmt.Errorf("invalid prior deletion record %q", path)
			}
			if previous.Author.ID == owner.ID && previous.Post.ID == ref.ID() && previous.SHA256 == *expectHash && previous.Deleted && previous.Verified {
				return writeJSON(out, previous)
			}
			return fmt.Errorf("an earlier deletion attempt exists at %q; inspect it before retrying; no POST made", path)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	post, err := c.ReadPost(ctx, ref)
	if err != nil {
		return err
	}
	if post.Author.ID != owner.ID {
		return fmt.Errorf("post %q does not belong to the selected account", post.ID)
	}
	result := deletionFor(owner, post)
	if !*apply {
		return writeJSON(out, result)
	}
	if result.SHA256 != *expectHash {
		return fmt.Errorf("post %q changed; review it again before deleting", post.ID)
	}
	return deletePackage(ctx, c, dir, result, out)
}

func deletePackage(ctx context.Context, c *service.Client, dir string, result deletionPreview, out io.Writer) error {
	if result.Author.ID != c.Owner().ID || result.Post.Author.ID != c.Owner().ID {
		return fmt.Errorf("post %q does not belong to the selected account", result.Post.ID)
	}
	if err := c.PrepareDeletionMetadata(ctx); err != nil {
		return fmt.Errorf("prepare deletion before reserving an attempt; no POST sent: %w", err)
	}
	history := filepath.Join(dir, "deletions")
	if err := os.MkdirAll(history, 0700); err != nil {
		return err
	}
	path := filepath.Join(history, result.Post.ID+".json")
	result.AttemptPath = path
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("deletion attempt already exists or cannot be saved at %q: %w", path, err)
	}
	writeErr := writeJSON(f, result)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	receipt, err := os.OpenFile(path+".receipt.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer receipt.Close()
	if err := c.DeletePost(ctx, result.Post, receipt); err != nil {
		return fmt.Errorf("deletion unconfirmed; inspect %q before retrying: %w", path, err)
	}
	result.Deleted, result.Verified = true, true
	tmp, err := os.CreateTemp(history, ".deleted-*")
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
