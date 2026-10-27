package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"github.com/meoyawn/clis/linkedin/internal/service"
	"golang.org/x/net/html"
)

func (c *linkedinClient) resolvePost(ctx context.Context, value string) (domain.PostRef, error) {
	if ref, err := domain.NewPostRef(value); err == nil {
		return ref, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "lnkd.in" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/p/") || len(strings.Split(strings.Trim(u.Path, "/"), "/")) != 2 {
		return domain.NewPostRef(value)
	}
	// Resolve only LinkedIn's own short links, without sending account cookies or
	// following the destination. The authenticated client never visits lnkd.in.
	reader := *c.http
	reader.Jar = nil
	reader.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	if err != nil {
		return domain.PostRef{}, err
	}
	resp, err := reader.Do(req)
	if err != nil {
		return domain.PostRef{}, fmt.Errorf("resolve LinkedIn short link: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return domain.PostRef{}, fmt.Errorf("LinkedIn short link %q returned HTTP %d instead of a redirect", value, resp.StatusCode)
	}
	destination, err := resp.Location()
	if err != nil || destination.Scheme != "https" || destination.Host != "www.linkedin.com" || destination.User != nil {
		return domain.PostRef{}, fmt.Errorf("LinkedIn short link %q has an unsupported destination", value)
	}
	destination.RawQuery, destination.Fragment = "", ""
	if ref, err := domain.NewPostRef(destination.String()); err == nil {
		return ref, nil
	}
	if !strings.HasPrefix(destination.Path, "/posts/") {
		return domain.PostRef{}, fmt.Errorf("LinkedIn short link destination %q is not a post permalink", destination.String())
	}
	page, err := c.get(ctx, destination.RequestURI(), http.Header{"Accept": {"text/html"}})
	if err != nil {
		return domain.PostRef{}, err
	}
	defer page.Body.Close()
	doc, err := html.Parse(io.LimitReader(page.Body, 16<<20))
	if err != nil {
		return domain.PostRef{}, err
	}
	refs := map[string]domain.PostRef{}
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || attr(n, "aria-label") != "Primary content" || connectionControlHidden(n) {
			continue
		}
		for child := range n.Descendants() {
			urn, ok := strings.CutPrefix(attr(child, "data-testid"), "ReactionFacepileCollection-")
			if !ok || connectionControlHidden(child) {
				continue
			}
			if ref, err := domain.NewPostRef(urn); err == nil {
				refs[ref.URN()] = ref
			}
		}
	}
	if len(refs) != 1 {
		return domain.PostRef{}, fmt.Errorf("LinkedIn short link destination exposes %d primary activities", len(refs))
	}
	for _, ref := range refs {
		return ref, nil
	}
	return domain.PostRef{}, fmt.Errorf("LinkedIn short link has no activity")
}

func runPostEdit(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("posts edit", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("text-file", "", "exact replacement commentary in UTF-8")
	capture := fs.String("save-response", "", "save the edit-contract response to a new private file")
	opts := postEditOptions{recordDir: filepath.Join(store.dir, "post-edits")}
	fs.BoolVar(&opts.apply, "apply", false, "apply the requested edit; otherwise preview")
	fs.StringVar(&opts.expectedAuthor, "expect-author", "", "author from preview")
	fs.StringVar(&opts.expectedHash, "expect-sha256", "", "replacement text hash from preview")
	fs.StringVar(&opts.expectedCurrentHash, "expect-current-sha256", "", "current text hash from preview")
	positionals, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(out, []string{"posts", "edit"})
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 || *file == "" {
		return fmt.Errorf("posts edit requires one exact post and --text-file")
	}
	text, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	draft, err := domain.NewPostDraft(string(text))
	if err != nil {
		return err
	}
	if opts.apply && (opts.expectedAuthor == "" || opts.expectedHash != draft.SHA256() || opts.expectedCurrentHash == "") {
		return fmt.Errorf("--apply requires matching --expect-author, --expect-sha256 and --expect-current-sha256 from preview")
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
	post, err := service.ParsePostDocument(ref, string(doc))
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
		return fmt.Errorf("post %q is not owned by the signed-in author %q", ref.URL(), author.URL)
	}
	root, err := html.Parse(bytes.NewReader(doc))
	if err != nil {
		return err
	}
	data := readDocumentData(root)
	actions := map[string]map[string]any{}
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] != "proto.sdui.actions.core.ServerRequest" || jsonStringAt(m, "value", "requestId") != "feedUpdateControlMenuRequest" {
				return
			}
			v, _ := m["value"].(map[string]any)
			if jsonStringAt(v, "requestedArguments", "payload", "threadUrn", "threadUrnActivityThreadUrn", "activityUrn", "activityId") != strings.TrimPrefix(ref.URN(), "urn:li:activity:") {
				return
			}
			encoded, _ := json.Marshal(v)
			actions[string(encoded)] = v
		})
	}
	if len(actions) != 1 {
		return fmt.Errorf("post %q exposes %d exact control-menu requests", ref.URL(), len(actions))
	}
	for _, action := range actions {
		response, err := c.postNativeAction(ctx, action, []any{})
		if err != nil {
			return err
		}
		menu := readFlightData(string(response))
		screens := map[string]map[string]any{}
		for _, row := range menu {
			menu.walk(row, func(m map[string]any) {
				if m["$type"] != "proto.sdui.actions.core.NavigateToScreen" || m["screenId"] != "com.linkedin.sdui.flagshipnav.sharing.ShareCompose" {
					return
				}
				if jsonStringAt(m, "requestedArguments", "payload", "shareUrnLegacy") != jsonStringAt(action, "requestedArguments", "payload", "rootUrn") {
					return
				}
				encoded, _ := json.Marshal(m)
				screens[string(encoded)] = m
			})
		}
		if len(screens) != 1 {
			return fmt.Errorf("post menu exposes %d exact edit screens", len(screens))
		}
		for _, screen := range screens {
			response, err = c.postEditScreen(ctx, screen)
			if err != nil {
				return err
			}
		}
		if *capture != "" {
			f, err := os.OpenFile(*capture, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			_, writeErr := f.Write(response)
			closeErr := f.Close()
			if err := errors.Join(writeErr, closeErr); err != nil {
				return err
			}
		}
		plan, err := preparePostEdit(post, draft, response)
		if err != nil {
			return err
		}
		result, err := c.applyPostEdit(ctx, ref, author.person, post, draft, plan, opts)
		if err != nil {
			return err
		}
		return writeJSON(out, result)
	}
	return fmt.Errorf("control-menu request missing")
}

func (c *linkedinClient) postEditScreen(ctx context.Context, screen map[string]any) ([]byte, error) {
	const id = "com.linkedin.sdui.flagshipnav.sharing.ShareCompose"
	args, _ := screen["requestedArguments"].(map[string]any)
	clientArgs := map[string]any{}
	for key, value := range args {
		clientArgs[key] = value
	}
	clientArgs["states"], clientArgs["screenId"], clientArgs["knownTemplateIds"] = []any{}, id, []any{}
	body, err := json.Marshal(map[string]any{"clientArguments": clientArgs, "isModal": true})
	if err != nil {
		return nil, err
	}
	resp, err := c.request(ctx, http.MethodPost, "/flagship-web/rsc-action/actions/navigation?screenId="+id+"&sduiid="+id, bytes.NewReader(body), http.Header{"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "X-Li-Rsc-Stream": {"true"}, "X-Li-Layout-Tree": {id}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func (c *linkedinClient) postNativeAction(ctx context.Context, action map[string]any, states []any) ([]byte, error) {
	id, _ := action["requestId"].(string)
	args, _ := action["requestedArguments"].(map[string]any)
	clientArgs := map[string]any{}
	for key, value := range args {
		clientArgs[key] = value
	}
	screenID := "com.linkedin.sdui.flagshipnav.feed.MainFeed"
	if id != "feedUpdateControlMenuRequest" {
		screenID = "com.linkedin.sdui.flagshipnav.sharing.ShareCompose"
	}
	clientArgs["states"], clientArgs["screenId"], clientArgs["knownTemplateIds"] = states, screenID, []any{}
	body, err := json.Marshal(map[string]any{"requestId": id, "serverRequest": action, "states": states, "requestedArguments": clientArgs})
	if err != nil {
		return nil, err
	}
	resp, err := c.request(ctx, http.MethodPost, "/flagship-web/rsc-action/actions/server-request?sduiid="+url.QueryEscape(id), bytes.NewReader(body), http.Header{"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"}, "X-Li-Actor": {"Self"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("native response exceeds 16 MiB")
	}
	return data, nil
}
