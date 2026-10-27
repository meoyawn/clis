package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

const Origin = "https://x.com"
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"

type Client struct {
	http          *http.Client
	session       Session
	bearer        string
	operations    map[string]string
	owner         domain.Profile
	threadCapture io.Writer
}

func NewClient(httpClient *http.Client, session Session) *Client {
	return &Client{http: httpClient, session: session}
}

// request is deliberately single-attempt. Cookies never leave x.com and its
// observed upload host; redirects are refused by the owning HTTP client.
func (c *Client) request(ctx context.Context, method, target, contentType string, body []byte, extraHeaders ...http.Header) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Scheme != "https" || (u.Host != "x.com" && u.Host != "upload.x.com" && u.Host != "abs.twimg.com") {
		return nil, fmt.Errorf("unsupported request destination %q", target)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if u.Host == "x.com" || u.Host == "upload.x.com" {
		for _, cookie := range []*http.Cookie{{Name: "auth_token", Value: c.session.AuthToken}, {Name: "ct0", Value: c.session.CSRF}, {Name: "twid", Value: c.session.TWID}} {
			req.AddCookie(cookie)
		}
		req.Header.Set("Origin", Origin)
		req.Header.Set("Referer", Origin+"/home")
		if c.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearer)
			req.Header.Set("X-CSRF-Token", c.session.CSRF)
			req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
			req.Header.Set("X-Twitter-Active-User", "yes")
			req.Header.Set("X-Twitter-Client-Language", "en")
		}
	}
	for _, headers := range extraHeaders {
		for key, values := range headers {
			req.Header[key] = values
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: transport failed; no automatic retry", method, u.Path)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", u.Path, err)
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("response for %s exceeds 16 MiB", u.Path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("%s %s: HTTP %d; Retry-After=%q; stop and inspect the account before retrying", method, u.Path, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	return data, nil
}

var mainScript = regexp.MustCompile(`https://abs\.twimg\.com/responsive-web/client-web(?:-legacy)?/main\.[A-Za-z0-9]+\.js`)
var publicBearer = regexp.MustCompile(`AAAAA[A-Za-z0-9%]{60,}`)
var operationRecord = regexp.MustCompile(`queryId:"([A-Za-z0-9_-]+)",operationName:"([A-Za-z0-9_]+)"`)

func (c *Client) Bootstrap(ctx context.Context) (domain.Profile, error) {
	if err := c.session.Validate(); err != nil {
		return domain.Profile{}, err
	}
	data, err := c.request(ctx, http.MethodGet, Origin+"/home", "", nil)
	if err != nil {
		return domain.Profile{}, err
	}
	// The initial document contains the authenticated account's settings. It is
	// parsed as data only; browser scripts and telemetry are never executed.
	state, err := initialState(data)
	if err != nil {
		return domain.Profile{}, err
	}
	var settings struct {
		ScreenName string `json:"screen_name"`
	}
	if err := json.Unmarshal(state.Settings.Remote.Settings, &settings); err != nil || settings.ScreenName == "" {
		return domain.Profile{}, fmt.Errorf("the X bootstrap does not expose a signed-in account; sign in at x.com")
	}
	if _, err := domain.Handle(settings.ScreenName); err != nil {
		return domain.Profile{}, err
	}
	script := mainScript.FindString(string(data))
	if script == "" {
		return domain.Profile{}, fmt.Errorf("the X bootstrap main bundle is unavailable; inspect the current browser contract")
	}
	bundle, err := c.request(ctx, http.MethodGet, script, "", nil)
	if err != nil {
		return domain.Profile{}, err
	}
	token := publicBearer.FindString(string(bundle))
	if token == "" {
		return domain.Profile{}, fmt.Errorf("the X public client authorization is unavailable in its main bundle")
	}
	c.bearer, err = url.QueryUnescape(token)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("invalid public client authorization encoding")
	}
	c.operations = map[string]string{}
	for _, m := range operationRecord.FindAllStringSubmatch(string(bundle), -1) {
		c.operations[m[2]] = m[1]
	}
	for _, name := range []string{"UserByScreenName", "TweetDetail", "CreateTweet", "UserOriginalsTimeline"} {
		if c.operations[name] == "" {
			return domain.Profile{}, fmt.Errorf("the X bundle has no %s operation; inspect the current contract", name)
		}
	}
	owner, err := c.Profile(ctx, settings.ScreenName)
	if err != nil {
		return domain.Profile{}, err
	}
	if owner.ID != c.session.UserID() {
		return domain.Profile{}, fmt.Errorf("signed-in bootstrap profile does not match selected Chrome cookie identity")
	}
	c.owner = owner
	return owner, nil
}

type bootstrapState struct {
	Settings struct {
		Remote struct {
			Settings json.RawMessage `json:"settings"`
		} `json:"remote"`
	} `json:"settings"`
}

func initialState(data []byte) (bootstrapState, error) {
	marker := []byte("window.__INITIAL_STATE__=")
	_, tail, ok := bytes.Cut(data, marker)
	if !ok {
		return bootstrapState{}, fmt.Errorf("the X bootstrap does not contain initial account data")
	}
	var state bootstrapState
	if err := json.NewDecoder(bytes.NewReader(tail)).Decode(&state); err != nil {
		return state, fmt.Errorf("decode X bootstrap account data: %w", err)
	}
	return state, nil
}

// Captured read/write feature switches on 27 October 2026. Persisted query IDs
// and the public web-client token are discovered, never saved in the checkout.
var features = map[string]bool{
	"premium_content_api_read_enabled": false, "communities_web_enable_tweet_community_results_fetch": true, "c9s_tweet_anatomy_moderator_badge_enabled": true, "responsive_web_grok_analyze_button_fetch_trends_enabled": false, "responsive_web_grok_analyze_post_followups_enabled": true, "rweb_cashtags_composer_attachment_enabled": true, "responsive_web_jetfuel_frame": true, "rweb_sports_post_context_enabled": true, "responsive_web_grok_share_attachment_enabled": true, "responsive_web_grok_annotations_enabled": true, "responsive_web_edit_tweet_api_enabled": true, "rweb_conversational_replies_downvote_enabled": false, "graphql_is_translatable_rweb_tweet_is_translatable_enabled": true, "view_counts_everywhere_api_enabled": true, "longform_notetweets_consumption_enabled": true, "responsive_web_twitter_article_tweet_consumption_enabled": true, "content_disclosure_indicator_enabled": true, "content_disclosure_ai_generated_indicator_enabled": true, "responsive_web_grok_show_grok_translated_post": true, "responsive_web_grok_analysis_button_from_backend": true, "post_ctas_fetch_enabled": false, "longform_notetweets_rich_text_read_enabled": true, "longform_notetweets_inline_media_enabled": false, "profile_label_improvements_pcf_label_in_post_enabled": true, "responsive_web_profile_redirect_enabled": true, "rweb_tipjar_consumption_enabled": false, "verified_phone_label_enabled": false, "responsive_web_nested_quote_preview_enabled": true, "articles_preview_enabled": true, "rweb_cashtags_enabled": true, "responsive_web_grok_community_note_auto_translation_is_enabled": true, "freedom_of_speech_not_reach_fetch_enabled": true, "standardized_nudges_misinfo": true, "tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true, "responsive_web_grok_image_annotation_enabled": true, "responsive_web_grok_imagine_annotation_enabled": true, "responsive_web_made_on_x_label_enabled": false, "responsive_web_graphql_timeline_navigation_enabled": true,
	"rweb_video_screen_enabled": false, "creator_subscriptions_tweet_preview_api_enabled": true, "responsive_web_enhance_cards_enabled": false, "hidden_profile_subscriptions_enabled": true, "subscriptions_verification_info_is_identity_verified_enabled": true, "subscriptions_verification_info_verified_since_enabled": true, "highlights_tweets_tab_ui_enabled": true, "responsive_web_twitter_article_notes_tab_enabled": true, "subscriptions_feature_can_gift_premium": false,
}

func (c *Client) query(ctx context.Context, operation domain.Operation, variables any, toggles any) ([]byte, error) {
	id := c.operations[operation.String()]
	if id == "" {
		return nil, fmt.Errorf("operation %q has not been bootstrapped", operation.String())
	}
	q := url.Values{}
	for k, v := range map[string]any{"variables": variables, "features": features, "fieldToggles": toggles} {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		q.Set(k, string(b))
	}
	data, err := c.request(ctx, http.MethodGet, Origin+"/i/api/graphql/"+id+"/"+operation.String()+"?"+q.Encode(), "application/json", nil)
	if err != nil {
		return nil, err
	}
	return data, graphQLError(data)
}

func graphQLError(data []byte) error {
	var result struct {
		Errors []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("the X returned non-JSON data")
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("the X rejected request with API code %d; stop without retrying", result.Errors[0].Code)
	}
	return nil
}

func HTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *Client) Owner() domain.Profile { return c.owner }

func (c *Client) CapturePostResponses(w io.Writer) { c.threadCapture = w }

func (c *Client) Profile(ctx context.Context, handle string) (domain.Profile, error) {
	if _, err := domain.Handle(handle); err != nil {
		return domain.Profile{}, err
	}
	data, err := c.query(ctx, domain.ProfileOperation(), map[string]any{"screen_name": handle, "withGrokTranslatedBio": true}, map[string]bool{"withPayments": false, "withAuxiliaryUserLabels": true})
	if err != nil {
		return domain.Profile{}, err
	}
	var body struct {
		Data struct {
			User struct {
				Result userRecord `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return domain.Profile{}, err
	}
	p, err := body.Data.User.Result.profile()
	if err == nil && !strings.EqualFold(p.Handle, handle) {
		err = fmt.Errorf("the X returned a different profile for %q", handle)
	}
	return p, err
}

type userRecord struct {
	Type string `json:"__typename"`
	ID   string `json:"rest_id"`
	Core struct {
		Name   string `json:"name"`
		Handle string `json:"screen_name"`
	} `json:"core"`
	Counts struct {
		Followers int64 `json:"followers"`
	} `json:"relationship_counts"`
}

func (u userRecord) profile() (domain.Profile, error) {
	if u.Type != "User" || u.ID == "" || u.Core.Handle == "" {
		return domain.Profile{}, fmt.Errorf("the X response does not contain an available user identity")
	}
	if _, err := domain.Handle(u.Core.Handle); err != nil {
		return domain.Profile{}, err
	}
	return domain.Profile{ID: u.ID, Handle: u.Core.Handle, Name: u.Core.Name, URL: Origin + "/" + u.Core.Handle, Followers: u.Counts.Followers}, nil
}
