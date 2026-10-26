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
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Captured from Chrome's "Send without a note" action on 2026-10-26.
// Request-stage interception cancelled the POST before it reached LinkedIn.
const connectPath = "/voyager/api/voyagerRelationshipsDashMemberRelationships?action=verifyQuotaAndCreateV2&decorationId=com.linkedin.voyager.dash.deco.relationships.InvitationCreationResultWithInvitee-3"

type connectionAvailability struct {
	CanConnect bool   `json:"can_connect"`
	Pending    bool   `json:"pending"`
	Evidence   string `json:"profile_evidence"`
}

type connectResult struct {
	Sender    person `json:"sender"`
	Recipient person `json:"recipient"`
	Note      string `json:"note"`
	Sent      bool   `json:"sent"`
	connectionAvailability
	CheckedAt string `json:"checked_at"`
}

func runConnect(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("people connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _ = writeHelp(stderr, []string{"people", "connect"}) }
	send := fs.Bool("send", false, "send the approved connection request without a note; otherwise preview only")
	positionals, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(stdout, []string{"people", "connect"})
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("people connect requires one recipient profile; got %d arguments", len(positionals))
	}
	ref, err := parseProfile(positionals[0])
	if err != nil {
		return err
	}
	client, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	result, err := client.connect(ctx, ref, *send)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func (c *linkedinClient) connect(ctx context.Context, ref profileRef, send bool) (connectResult, error) {
	recipient, err := c.profile(ctx, ref)
	if err != nil {
		return connectResult{}, err
	}
	if ref.ID != "" && recipient.ProfileURN != "urn:li:fsd_profile:"+ref.ID {
		return connectResult{}, fmt.Errorf("connection target %q resolved to a different profile %q", ref.ID, recipient.ProfileURN)
	}
	selfURN, err := c.mailbox(ctx)
	if err != nil {
		return connectResult{}, err
	}
	if recipient.ProfileURN == selfURN {
		return connectResult{}, fmt.Errorf("cannot connect to your own profile %q", recipient.URL)
	}
	sender, err := c.profile(ctx, profileRef{ID: strings.TrimPrefix(selfURN, "urn:li:fsd_profile:")})
	if err != nil {
		return connectResult{}, fmt.Errorf("identify connection sender: %w", err)
	}
	if sender.ProfileURN != selfURN {
		return connectResult{}, fmt.Errorf("connection sender response does not match signed-in profile %q", selfURN)
	}
	state, err := c.connectionAvailability(ctx, recipient.person)
	if err != nil {
		return connectResult{}, err
	}
	result := connectResult{Sender: sender.person, Recipient: recipient.person, connectionAvailability: state, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if !send || state.Pending {
		return result, nil
	}
	// No note, tracking payload, guessed invitation ID, or alternate endpoint.
	body, err := json.Marshal(map[string]any{"invitee": map[string]any{"inviteeUnion": map[string]string{"memberProfile": recipient.ProfileURN}}})
	if err != nil {
		return connectResult{}, fmt.Errorf("encode connection request: %w", err)
	}
	resp, err := c.request(ctx, http.MethodPost, connectPath, bytes.NewReader(body), http.Header{
		"Content-Type": {"application/json; charset=UTF-8"},
		"Accept":       {"application/vnd.linkedin.normalized+json+2.1"},
	})
	if err != nil {
		return connectResult{}, fmt.Errorf("connection request failed; do not resend without checking the target's state: %w", err)
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if readErr != nil {
		return connectResult{}, fmt.Errorf("connection request outcome is unconfirmed; inspect %q before retrying: %w", recipient.URL, readErr)
	}
	// HTTP success alone is not a sent receipt. Read the exact target's current
	// Pending control once; never retry the POST or infer acceptance.
	state, err = c.connectionAvailability(ctx, recipient.person)
	if err != nil {
		return connectResult{}, fmt.Errorf("connection request outcome is unconfirmed; inspect %q before retrying: %w", recipient.URL, err)
	}
	if !state.Pending {
		return connectResult{}, fmt.Errorf("connection request outcome is unconfirmed: %q did not show Pending; inspect before retrying", recipient.URL)
	}
	result.connectionAvailability = state
	result.Sent = true
	result.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	return result, nil
}

func (c *linkedinClient) connectionAvailability(ctx context.Context, target person) (connectionAvailability, error) {
	body, err := c.profileDocument(ctx, target.URL)
	if err != nil {
		return connectionAvailability{}, err
	}
	return parseConnectionAvailability(body, target)
}

func parseConnectionAvailability(body string, target person) (connectionAvailability, error) {
	memberID, ok := strings.CutPrefix(target.MemberURN, "urn:li:member:")
	if !ok || !digits.MatchString(memberID) {
		return connectionAvailability{}, fmt.Errorf("connection target %q has invalid member identifier %q", target.URL, target.MemberURN)
	}
	ref, err := parseProfile(target.URL)
	if err != nil {
		return connectionAvailability{}, err
	}
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return connectionAvailability{}, fmt.Errorf("parse connection target %q: %w", target.URL, err)
	}
	var primary *html.Node
	var title string
	for n := range root.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		if n.Data == "title" {
			title = nodeText(n)
		}
		if primary == nil && (attr(n, "aria-label") == "Primary content" || n.Data == "main" || attr(n, "role") == "main") {
			primary = n
		}
	}
	if primary == nil || !strings.HasPrefix(title, target.Name+" | LinkedIn") {
		return connectionAvailability{}, fmt.Errorf("connection document does not confirm target %q and its primary content; no invitation sent", target.URL)
	}
	var state connectionAvailability
	prefix := "ConnectButtonstate:invitation:" + target.MemberURN
	for n := range primary.Descendants() {
		if n.Type != html.ElementNode || (n.Data != "a" && n.Data != "button") || connectionControlHidden(n) {
			continue
		}
		label := attr(n, "aria-label")
		switch attr(n, "componentkey") {
		case prefix + "_connect":
			if label != "Invite "+target.Name+" to connect" || attr(n, "aria-disabled") != "false" {
				continue
			}
			u, err := url.Parse(attr(n, "href"))
			if err != nil || u.User != nil || (u.Host != "" && u.Host != "www.linkedin.com") || (u.Scheme != "" && u.Scheme != "https") ||
				u.Path != "/preload/custom-invite/" || len(u.Query()["vanityName"]) != 1 || u.Query().Get("vanityName") != ref.Slug {
				continue
			}
			state.CanConnect, state.Evidence = true, label
		case prefix + "_pending":
			if label == "Pending, click to withdraw invitation sent to "+target.Name {
				state.Pending, state.Evidence = true, label
			}
		}
	}
	if state.CanConnect == state.Pending {
		return connectionAvailability{}, fmt.Errorf("profile %q does not expose one supported target-specific Connect or Pending state; existing connections, other invitation flows and changed responses are not sent", target.URL)
	}
	return state, nil
}

func connectionControlHidden(n *html.Node) bool {
	for p := n; p != nil; p = p.Parent {
		if p.Data == "template" || p.Data == "script" || p.Data == "style" || attr(p, "aria-hidden") == "true" {
			return true
		}
		for _, a := range p.Attr {
			if a.Key == "hidden" || a.Key == "disabled" {
				return true
			}
		}
		style := strings.ToLower(strings.ReplaceAll(attr(p, "style"), " ", ""))
		if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") {
			return true
		}
	}
	return false
}
