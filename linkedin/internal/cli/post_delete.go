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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"github.com/meoyawn/clis/linkedin/internal/service"
	"golang.org/x/net/html"
)

func (c *linkedinClient) postControlMenu(ctx context.Context, ref domain.PostRef, doc []byte) ([]byte, error) {
	root, err := html.Parse(bytes.NewReader(doc))
	if err != nil {
		return nil, err
	}
	data := readDocumentData(root)
	actions := map[string]map[string]any{}
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] != "proto.sdui.actions.core.ServerRequest" || jsonStringAt(m, "value", "requestId") != "feedUpdateControlMenuRequest" {
				return
			}
			v, _ := m["value"].(map[string]any)
			if !postMenuMatches(ref, v) {
				return
			}
			encoded, _ := json.Marshal(v)
			actions[string(encoded)] = v
		})
	}
	if len(actions) != 1 {
		return nil, fmt.Errorf("post %q exposes %d exact control-menu requests", ref.URL(), len(actions))
	}
	for _, action := range actions {
		return c.postNativeAction(ctx, action, []any{})
	}
	return nil, fmt.Errorf("post control menu is missing")
}

func runPostDelete(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("posts delete", flag.ContinueOnError)
	fs.SetOutput(errOut)
	capture := fs.String("save-response", "", "save the exact post control menu to a new private file")
	apply := fs.Bool("apply", false, "delete the exact owned post; otherwise preview")
	expectedAuthor := fs.String("expect-author", "", "author from preview")
	expectedHash := fs.String("expect-sha256", "", "post package hash from preview")
	positionals, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(out, []string{"posts", "delete"})
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("posts delete requires one exact post")
	}
	c, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	ref, err := c.resolvePost(ctx, positionals[0])
	if err != nil {
		return err
	}
	doc, err := c.readPostDocument(ctx, ref, nil)
	if err != nil {
		return err
	}
	post, err := parsePostPage(ref, doc)
	if err != nil {
		return err
	}
	self, err := c.mailbox(ctx)
	if err != nil {
		return err
	}
	author, err := c.profile(ctx, profileRef{ID: strings.TrimPrefix(self, "urn:li:fsd_profile:")})
	if err != nil {
		return err
	}
	if author.ProfileURN != self || post.AuthorURL != author.URL {
		return fmt.Errorf("post %q is not owned by signed-in author %q", ref.URL(), author.URL)
	}
	menu, err := c.postControlMenu(ctx, ref, doc)
	if err != nil {
		return err
	}
	if *capture != "" {
		f, err := os.OpenFile(*capture, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(menu)
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
	}
	action, err := preparePostDelete(ref, menu)
	if err != nil {
		return err
	}
	packageHash := fmt.Sprintf("%x", sha256.Sum256(mustJSON(post)))
	result := map[string]any{"post": post, "author": author.URL, "sha256": packageHash, "deleted": false, "verified": false}
	if !*apply {
		return writeJSON(out, result)
	}
	if *expectedAuthor != author.URL || *expectedHash != packageHash {
		return fmt.Errorf("post deletion author or content differs from preview; no deletion sent")
	}
	recordDir := filepath.Join(store.dir, "post-deletions")
	if err := os.MkdirAll(recordDir, 0700); err != nil {
		return err
	}
	recordPath := filepath.Join(recordDir, strings.TrimPrefix(ref.URN(), "urn:li:activity:")+".json")
	f, err := os.OpenFile(recordPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("reserve deletion attempt; inspect any prior record before retrying: %w", err)
	}
	writeErr := writeJSON(f, map[string]any{"post": post, "sha256": packageHash, "action": action, "attempted_at": time.Now().UTC().Format(time.RFC3339Nano)})
	syncErr, closeErr := f.Sync(), f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	response, err := c.postNativeAction(ctx, action, []any{})
	if err != nil {
		return fmt.Errorf("deletion outcome unconfirmed; inspect %q before retrying: %w", recordPath, err)
	}
	if err := os.WriteFile(recordPath+".receipt", response, 0600); err != nil {
		return err
	}
	if _, _, err := editResponse(response); err != nil {
		return fmt.Errorf("delete response is unconfirmed; inspect %q: %w", recordPath, err)
	}
	after, err := c.readPostDocument(ctx, ref, nil)
	if err != nil {
		return fmt.Errorf("deletion acknowledged but readback failed; do not resend: %w", err)
	}
	if err := os.WriteFile(recordPath+".after.html", after, 0600); err != nil {
		return err
	}
	if _, err := service.ParsePostDocument(ref, string(after)); err == nil {
		return fmt.Errorf("deletion acknowledged but post remains visible; inspect %q before retrying", recordPath)
	}
	if !bytes.Contains(after, []byte("This post cannot be displayed")) && !bytes.Contains(after, []byte("This content isn’t available")) && !bytes.Contains(after, []byte("This content isn't available")) && !bytes.Contains(after, []byte("Post not found")) {
		return fmt.Errorf("deletion acknowledged but unavailable-post readback is unrecognized; inspect %q", recordPath)
	}
	result["deleted"], result["verified"], result["record_path"] = true, true, recordPath
	if err := os.WriteFile(recordPath+".verified.json", mustJSON(result), 0600); err != nil {
		return err
	}
	return writeJSON(out, result)
}

func preparePostDelete(ref domain.PostRef, response []byte) (map[string]any, error) {
	data := readFlightData(string(response))
	actions := map[string]map[string]any{}
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] != "proto.sdui.actions.core.ServerRequest" || jsonStringAt(m, "value", "requestId") != "com.linkedin.sdui.update.deletePost" {
				return
			}
			v, _ := m["value"].(map[string]any)
			args, _ := v["requestedArguments"].(map[string]any)
			payload, _ := args["payload"].(map[string]any)
			container, _ := payload["updateKeyContainer"].(map[string]any)
			items, _ := container["items"].([]any)
			if len(items) != 1 || jsonStringAt(items[0], "feedUpdateUrn", "updateUrnActivityUrn", "activityUrn", "activityId") != strings.TrimPrefix(ref.URN(), "urn:li:activity:") {
				return
			}
			keys, _ := args["requestedStateKeys"].([]any)
			if len(keys) != 0 {
				return
			}
			encoded, _ := json.Marshal(v)
			actions[string(encoded)] = v
		})
	}
	if len(actions) != 1 {
		return nil, fmt.Errorf("post %q exposes %d exact deletion actions", ref.URL(), len(actions))
	}
	for _, action := range actions {
		return action, nil
	}
	return nil, fmt.Errorf("post deletion action is missing")
}
