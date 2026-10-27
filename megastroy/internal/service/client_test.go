package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Serve captured website responses over real HTTP without reaching Megastroy.
// The production client still owns cookies, headers, redirects, and parsing.
type localTransport struct {
	destination *url.URL
	upstream    http.RoundTripper
}

func (l localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = l.destination.Scheme, l.destination.Host
	clone.URL = &u
	return l.upstream.RoundTrip(clone)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func servedClient(t *testing.T, body string, status int, headers http.Header, inspect func(*http.Request)) (*Client, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if inspect != nil {
			inspect(r)
		}
		for k, vs := range headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	session := Session{Version: 1, Browser: "chrome", ImportedAt: time.Now(), Cookies: []Cookie{
		{Name: "__ddg2_", Value: "test-clearance", Domain: ".megastroy.com", Path: "/", Secure: true},
	}}
	client, err := NewClient(session)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = localTransport{destination: destination, upstream: client.http.Transport}
	return client, &requests
}

func TestSearchCatalog(t *testing.T) {
	tests := []struct {
		name, category, brand, firstID string
		page, total                    int
		path, children                 []string
		widthType                      string
		hasMore                        bool
	}{
		{"search", "", "", "321780", 1, 167, []string{}, []string{"7", "8", "10"}, "select", true},
		{"parent", "255", "", "321780", 1, 151, []string{"8", "255"}, []string{"1102", "1103", "1263"}, "select", true},
		{"water", "1102", "", "321780", 1, 36, []string{"8", "255", "1102"}, []string{}, "range", true},
		{"brand", "1102", "Terminus", "321780", 1, 23, []string{"8", "255", "1102"}, []string{}, "range", false},
		{"page2", "", "", "460810", 2, 167, []string{}, []string{"7", "8", "10"}, "select", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filters := url.Values{}
			if tt.category != "" {
				filters.Set("category_id", tt.category)
			}
			if tt.brand != "" {
				filters.Set("field79[]", tt.brand)
			}
			client, requests := servedClient(t, fixture(t, tt.name), 200, nil, func(r *http.Request) {
				q := r.URL.Query()
				if r.Method != "GET" || r.URL.Path != "/catalog/search" || q.Get("q") != "полотенцесушитель" || q.Get("category_id") != tt.category || q.Get("field79[]") != tt.brand {
					t.Errorf("wrong search request: %s %s", r.Method, r.URL)
				}
				if (tt.page == 1 && q.Has("page")) || (tt.page == 2 && q.Get("page") != "1") {
					t.Errorf("wrong website page offset: %q", q.Get("page"))
				}
				cookie, err := r.Cookie("__ddg2_")
				if err != nil || cookie.Value != "test-clearance" || r.UserAgent() != DefaultUserAgent {
					t.Error("anonymous session or user agent missing")
				}
			})
			got, err := client.Search(context.Background(), SearchOptions{Query: "полотенцесушитель", Page: tt.page, IncludeFilters: true, Filters: filters, Sort: Popular()})
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 2
			if tt.name == "search" {
				wantCount = 3
			}
			if got.Total != tt.total || got.Count != wantCount || got.Results[0].ID != tt.firstID || got.Page != tt.page {
				t.Fatalf("incorrect search page: %+v", got)
			}
			if requests.Load() != 1 || got.HTTPRequests != 1 || got.HasMore != tt.hasMore {
				t.Fatalf("requests=%d metadata=%d has_more=%v", requests.Load(), got.HTTPRequests, got.HasMore)
			}
			if tt.hasMore && (got.NextPage == nil || *got.NextPage != tt.page+1 || got.NextURL == nil) {
				t.Fatal("missing continuation without fetching the next page")
			}
			path, children := []string{}, []string{}
			for i, c := range got.Categories.Path {
				path = append(path, c.ID)
				if c.Selected != (i == len(got.Categories.Path)-1) || (i > 0 && c.ParentID != path[i-1]) {
					t.Errorf("wrong ancestor or selection: %+v", c)
				}
			}
			for _, c := range got.Categories.Children {
				children = append(children, c.ID)
			}
			if !reflect.DeepEqual(path, tt.path) || !reflect.DeepEqual(children, tt.children) {
				t.Errorf("category path=%v children=%v", path, children)
			}
			widthFound, brandFound := false, tt.brand == ""
			for _, f := range *got.Filters {
				if strings.TrimSuffix(f.Key, "[]") == "field7" {
					widthFound = true
					maxWidth := 710.0
					if tt.brand != "" {
						maxWidth = 600
					}
					if f.Type != tt.widthType {
						t.Errorf("width filter should be %s, got %s", tt.widthType, f.Type)
					}
					if f.Type == "range" && (f.FromKey != "field7[from]" || f.ToKey != "field7[to]" || f.Min == nil || *f.Min != 400 || f.Max == nil || *f.Max != maxWidth) {
						t.Errorf("incorrect width bounds: %+v", f)
					}
				}
				seen := map[string]bool{}
				for _, option := range f.Options {
					if seen[option.Value] {
						t.Errorf("duplicated mobile/desktop filter option: %s=%s", f.Key, option.Value)
					}
					seen[option.Value] = true
					if f.Key == "field79[]" && option.Value == tt.brand && option.Selected {
						brandFound = true
					}
				}
			}
			if !widthFound || !brandFound {
				t.Error("missing dimension filter or selected brand")
			}
			if tt.firstID == "321780" {
				p := got.Results[0]
				if p.Price == nil || p.Price.Amount != 5290 || p.Price.Currency != "RUB" || p.OldPrice != nil || p.Available == nil || !*p.Available || len(p.Stores) != 4 || p.Stores[0].Quantity == nil || *p.Stores[0].Quantity != 2 {
					t.Errorf("wrong card price or stock: %+v", p)
				}
			}
			if tt.name == "search" {
				first, unrated, fourStars := got.Results[0], got.Results[1], got.Results[2]
				if first.Rating == nil || *first.Rating != 5 || first.ReviewCount == nil || *first.ReviewCount != 1 || unrated.Rating != nil || unrated.ReviewCount != nil || fourStars.Rating == nil || *fourStars.Rating != 4 || fourStars.ReviewCount == nil || *fourStars.ReviewCount != 2 {
					t.Errorf("incorrect published ratings: first=%v unrated=%v four_stars=%v", first.Rating, unrated.Rating, fourStars.Rating)
				}
			}
		})
	}
}

func TestEmptySearchExcludesRecommendations(t *testing.T) {
	for _, tt := range []struct {
		fixture, query string
		status         int
	}{
		{"empty", "zzqqxxnonexistentmegastroy", 200},
		{"empty-range", "полотенцесушитель", 404},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			client, requests := servedClient(t, fixture(t, tt.fixture), tt.status, nil, nil)
			got, err := client.Search(context.Background(), SearchOptions{Query: tt.query, Page: 1, IncludeFilters: true})
			if err != nil {
				t.Fatal(err)
			}
			if got.Total != 0 || got.Count != 0 || len(got.Results) != 0 || got.Results == nil || got.HasMore || got.NextPage != nil || got.Message == "" || requests.Load() != 1 {
				t.Fatalf("recommendations presented as matches: %+v", got)
			}
		})
	}
}

func TestSearchLimitDoesNotFetchExtraData(t *testing.T) {
	client, requests := servedClient(t, fixture(t, "search"), 200, nil, nil)
	got, err := client.Search(context.Background(), SearchOptions{Query: "полотенцесушитель", Page: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || len(got.Results) != 1 || got.Total != 167 || got.NextPage == nil || *got.NextPage != 2 || len(got.Warnings) == 0 || got.Filters != nil || got.Categories != nil || got.Sorting != nil || requests.Load() != 1 {
		t.Fatalf("incorrect limit or filter omission: %+v", got)
	}
}

func TestSearchRejectsUnconfirmedSelections(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts SearchOptions
	}{
		{"wrong query", SearchOptions{Query: "шуруп", Page: 1}},
		{"ignored category", SearchOptions{Query: "полотенцесушитель", Page: 1, Filters: url.Values{"category_id": {"1102"}}}},
		{"ignored checkbox", SearchOptions{Query: "полотенцесушитель", Page: 1, Filters: url.Values{"field79[]": {"Terminus"}}}},
		{"ignored range", SearchOptions{Query: "полотенцесушитель", Page: 1, Filters: url.Values{"price[from]": {"7000"}}}},
		{"ignored sort", SearchOptions{Query: "полотенцесушитель", Page: 1, Sort: PriceDescending()}},
		{"wrong page", SearchOptions{Query: "полотенцесушитель", Page: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := servedClient(t, fixture(t, "search"), 200, nil, nil)
			got, err := client.Search(context.Background(), tt.opts)
			if err == nil || got != nil {
				t.Fatalf("unconfirmed selection accepted: result=%v err=%v", got, err)
			}
		})
	}
}

func TestDetailsUsesActualCharacteristics(t *testing.T) {
	client, requests := servedClient(t, fixture(t, "details"), 200, nil, func(r *http.Request) {
		if r.URL.Path != "/products/416479" || r.URL.RawQuery != "" {
			t.Errorf("wrong details request: %s", r.URL)
		}
	})
	got, err := client.Details(context.Background(), Origin+"/products/416479")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "416479" || !strings.Contains(got.Name, "500х600") || got.Price == nil || got.Price.Amount != 7999 || got.OldPrice == nil || got.OldPrice.Amount != 8990 || got.PriceLabel != "Финальная цена" {
		t.Errorf("wrong product or prices: %+v", got.Product)
	}
	d := got.Dimensions
	if d.HeightMM == nil || *d.HeightMM != 600 || d.WidthMM == nil || *d.WidthMM != 532 || d.DepthMM == nil || *d.DepthMM != 84 || d.LengthMM != nil || d.DiameterMM != nil {
		t.Errorf("dimensions guessed from title or plumbing diameter: %+v", d)
	}
	if len(got.Characteristics) != 20 || len(d.Properties) != 6 || !strings.Contains(got.Description, "6 прочных перекладин") {
		t.Errorf("missing published characteristics or description: %+v", got)
	}
	if len(got.Images) != 3 || len(got.Stores) != 4 || got.Stores[3].Quantity == nil || *got.Stores[3].Quantity != 2 || len(got.Breadcrumbs) != 4 || got.Category == nil || got.Category.Title != "Водяные полотенцесушители" {
		t.Errorf("gallery, stock, or breadcrumb contamination: images=%d stores=%v breadcrumbs=%v", len(got.Images), got.Stores, got.Breadcrumbs)
	}
	if got.HTTPRequests != 1 || requests.Load() != 1 {
		t.Fatal("details fetched additional pages")
	}
	if got.Rating == nil || *got.Rating != 5 || got.ReviewCount == nil || *got.ReviewCount != 1 {
		t.Error("main product rating missing")
	}
}

func TestDetailsKeepsAmbiguousDimensionsUnresolved(t *testing.T) {
	body := `<div class="product js-counter-product-page" data-counter-product-id="123"><div class="product-head">
	<h1>Товар 500х600</h1><div class="product-code"><span class="js-codelink">123</span></div>
	<div id="content-description"><div class="collapse-table table-responsive">
	<div class="collapse-table__item"><div class="side">Высота, см</div><div class="side">60</div></div>
	<div class="collapse-table__item"><div class="side">Высота, мм</div><div class="side">610</div></div>
	<div class="collapse-table__item"><div class="side">Ширина</div><div class="side">50</div></div>
	<div class="collapse-table__item"><div class="side">Глубина</div><div class="side">8,4 см</div></div>
	<div class="collapse-table__item"><div class="side">Длина, м</div><div class="side">0,8</div></div>
	</div></div></div></div>`
	client, _ := servedClient(t, body, 200, nil, nil)
	got, err := client.Details(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	d := got.Dimensions
	if d.HeightMM != nil || d.WidthMM != nil || d.DepthMM == nil || *d.DepthMM != 84 || d.LengthMM == nil || *d.LengthMM != 800 || len(d.Properties) != 5 || got.Price != nil || got.Available != nil {
		t.Fatalf("unknown or conflicting values invented: %+v", got)
	}
}

func TestHTTPFailuresNeverRetryOrRenewSession(t *testing.T) {
	for _, tt := range []struct {
		name, body, message string
		status              int
		blocked             bool
		search              bool
	}{
		{"challenge", "<title>DDoS-Guard</title>", "DDoS-Guard", 200, true, false},
		{"search challenge", `<script data-ddg-origin="kazan.megastroy.com"></script>`, "DDoS-Guard", 503, true, true},
		{"forbidden", "Forbidden", "DDoS-Guard", 403, true, false},
		{"redirect", "redirect", "redirect not followed", 302, false, false},
		{"rate limit", "Too many requests", "HTTP 429", 429, false, false},
		{"not found", "<h1>Страница не найдена</h1>", "HTTP 404", 404, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{"Location": {Origin + "/"}, "Set-Cookie": {"__ddg2_=rejected; Path=/; Domain=.megastroy.com"}}
			client, requests := servedClient(t, tt.body, tt.status, headers, nil)
			var err error
			var hasResult bool
			target := Origin + "/products/416479"
			if tt.search {
				got, searchErr := client.Search(context.Background(), SearchOptions{
					Query: "Terminus", Page: 2, Filters: url.Values{"category_id": {"1102"}}, Sort: PriceAscending(),
				})
				err, hasResult = searchErr, got != nil
				target = Origin + "/catalog/search?category_id=1102&noredirect=1&page=1&q=Terminus&sort=price"
			} else {
				got, detailErr := client.Details(context.Background(), "416479")
				err, hasResult = detailErr, got != nil
			}
			if err == nil || hasResult || !strings.Contains(err.Error(), tt.message) || requests.Load() != 1 {
				t.Fatalf("unexpected failure handling: hasResult=%t err=%v requests=%d", hasResult, err, requests.Load())
			}
			var blocked *DDoSGuardError
			if errors.As(err, &blocked) != tt.blocked {
				t.Fatalf("wrong DDoS-Guard classification: %v", err)
			}
			if tt.blocked && blocked.URL != target {
				t.Errorf("lost blocked request URL: got %q, want %q", blocked.URL, target)
			}
			if client.Session().Cookies[0].Value != "test-clearance" {
				t.Error("failed response replaced saved clearance")
			}
		})
	}
}

func TestClientPersistsOnlyAnonymousRenewals(t *testing.T) {
	headers := http.Header{"Set-Cookie": {"__ddg8_=renewed; Path=/; Max-Age=1200", "PHPSESSID=account-session; Path=/"}}
	client, _ := servedClient(t, fixture(t, "details"), 200, headers, nil)
	if _, err := client.Details(context.Background(), "416479"); err != nil {
		t.Fatal(err)
	}
	cookies := client.Session().Cookies
	if len(cookies) != 2 || cookies[1].Name != "__ddg8_" || cookies[1].Value != "renewed" || cookies[1].Expires == nil || time.Until(*cookies[1].Expires) < 19*time.Minute {
		t.Fatalf("incorrect anonymous renewal: %+v", cookies)
	}
}

func TestInvalidRequestsStayOffline(t *testing.T) {
	for _, id := range []string{"0", "-1", "https://example.com/products/416479", Origin + "/catalog/416479"} {
		client, requests := servedClient(t, "", 200, nil, nil)
		if _, err := client.Details(context.Background(), id); err == nil || requests.Load() != 0 {
			t.Errorf("invalid product %q reached the network: %v", id, err)
		}
	}
	client, requests := servedClient(t, "", 200, nil, nil)
	if _, err := client.Search(context.Background(), SearchOptions{Query: "полотенцесушитель", Page: 1, Filters: url.Values{"q": {"other query"}}}); err == nil || requests.Load() != 0 {
		t.Fatalf("invalid filter reached the network: %v", err)
	}
}
