package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/dns-shop/internal/domain"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testClient(srv *httptest.Server) *Client {
	return &Client{http: srv.Client(), origin: srv.URL, session: Session{UserAgent: BrowserUserAgent}}
}

// The storefront redirects a bare query to its category and drops the sort,
// page and filters; prices arrive only from the product-buy POST.
func TestSearchRestoresDroppedParamsAndMergesPrices(t *testing.T) {
	var pages []url.Values
	var buy url.Values
	var csrf string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/":
			q := r.URL.Query()
			pages = append(pages, q)
			if q.Get("category") == "" {
				http.Redirect(w, r, "/search/?"+url.Values{"q": {q.Get("q")}, "category": {"17a8a26516404e77"}}.Encode(), http.StatusFound)
				return
			}
			_, _ = w.Write(fixture(t, "search.html"))
		case "/ajax-state/product-buy/":
			_ = r.ParseForm()
			buy, csrf = r.PostForm, r.Header.Get("X-CSRF-Token")
			_, _ = w.Write([]byte(`{"result":true,"data":{"states":[
				{"id":"as-EIa6ho","data":{"price":{"current":199,"previous":249}}},
				{"id":"as-nMLirI","data":{"price":{"current":160}}}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	o := Options{Page: 2, Limit: 1, Sort: mustSort(t, "price-asc"), Filters: url.Values{"brand": {"aceline", "dexp"}}}
	r, err := testClient(srv).Search(context.Background(), "кабель micro usb", o)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("page requests=%d, want redirect target plus one re-request", len(pages))
	}
	final := pages[2]
	if final.Get("order") != "price-asc" || final.Get("p") != "2" || final.Get("brand") != "aceline-dexp" || final.Get("category") != "17a8a26516404e77" {
		t.Fatalf("re-request lost parameters: %v", final)
	}
	var payload struct {
		Hash       string      `json:"hash"`
		Containers []container `json:"containers"`
	}
	if err = json.Unmarshal([]byte(buy.Get("data")), &payload); err != nil || payload.Hash != "0123456789abcdef" || len(payload.Containers) != 2 || csrf != "synthetic-csrf-token" {
		t.Fatalf("price request payload=%q csrf=%q", buy.Get("data"), csrf)
	}
	if r.Total == nil || *r.Total != 424 || r.Category != "Кабели для мобильных устройств" || !r.HasMore || r.PageSize != 2 || len(r.Products) != 1 {
		t.Fatalf("result=%+v", r)
	}
	p := r.Products[0]
	if p.Code != "9086540" || p.Price == nil || *p.Price != 160 || p.OldPrice != nil || p.Rating == nil || *p.Rating != 4.83 || p.ReviewCount == nil || *p.ReviewCount != 660 {
		t.Fatalf("first product=%+v", p)
	}
	if !strings.HasPrefix(p.URL, srv.URL+"/product/") || p.Title != "Кабель Aceline micro USB-B - USB Type-A белый" || len(p.Images) != 1 || len(p.Characteristics) == 0 {
		t.Fatalf("card fields=%+v", p)
	}
}

func TestSearchKeepsCrossedOutPriceByCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"result":true,"data":{"states":[{"id":"as-EIa6ho","data":{"price":{"current":199,"previous":249}}}]}}`))
			return
		}
		_, _ = w.Write(fixture(t, "search.html"))
	}))
	defer srv.Close()
	r, err := testClient(srv).Search(context.Background(), "кабель", Options{Page: 1, Category: "17a8a26516404e77"})
	if err != nil {
		t.Fatal(err)
	}
	second := r.Products[1]
	if r.Products[0].Price != nil || second.Code != "9086545" || second.OldPrice == nil || *second.OldPrice != 249 || *second.Price != 199 {
		t.Fatalf("products=%+v", r.Products)
	}
}

func TestDetailsCombinesCharacteristicsAndMicrodata(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/product/characteristics/"):
			_, _ = w.Write(fixture(t, "characteristics.json"))
		case r.URL.Path == "/product/microdata/ec2cd41a-774d-4160-8007-16571c119831/":
			_, _ = w.Write(fixture(t, "microdata.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	d, err := testClient(srv).Details(context.Background(), srv.URL+"/product/ec2cd41a774d1983/kabel-aceline-micro-usb-b-usb-type-a-belyj/")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || d.HTTPRequests != 2 {
		t.Fatalf("paths=%v", paths)
	}
	if d.Code != "9086540" || d.Brand != "Aceline" || d.Price == nil || *d.Price != 160 || d.Availability != "InStock" || len(d.Images) != 3 || d.Description == "" {
		t.Fatalf("details=%+v", d.Product)
	}
	want := domain.Characteristic{Group: "Заводские данные", Name: "Страна-производитель", Value: "Китай"}
	found := false
	for _, c := range d.Characteristics {
		found = found || c == want
	}
	if !found {
		t.Fatalf("grouped characteristic missing: %+v", d.Characteristics)
	}
}

func TestProtectionStopsAfterOneRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{{"unauthorized", 401, "<html>qrator</html>"}, {"challenge", 200, `<script src="/__qrator/qauth_utm_v2.js"></script>`}} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := testClient(srv).Search(context.Background(), "кабель", Options{Page: 1})
			var blocked *BlockedError
			if !errors.As(err, &blocked) || !strings.HasPrefix(blocked.URL, srv.URL+"/search/") || requests != 1 {
				t.Fatalf("err=%v requests=%d", err, requests)
			}
		})
	}
}

func TestSessionStaysPrivateAndRejectsAccountCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "session.json")
	s := Session{UserAgent: BrowserUserAgent, ImportedAt: time.Now(), Profile: "Default", Cookies: []SavedCookie{{Name: "qrator_jsid2", Value: "synthetic-secret", Domain: ".dns-shop.ru", Path: "/"}}}
	if err := SaveSession(path, s); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("stat=%v err=%v", info, err)
	}
	if restored, err := LoadSession(path); err != nil || len(restored.Cookies) != 1 {
		t.Fatalf("round trip=%+v err=%v", restored, err)
	}
	s.Cookies[0].Name = "auth_access_token"
	if err := SaveSession(path, s); err == nil {
		t.Fatal("account cookie was saved")
	}
}

func TestRequireCityReadsSignedCityCookie(t *testing.T) {
	// Synthetic Yii-signed value with the structure the storefront sets.
	signed := "deadbeef" + url.QueryEscape(`a:2:{i:0;s:12:"current_path";i:1;s:96:"{"city":"55506b55-0565-11df-9cf0-00151716f9f5","cityName":"Казань","method":"manual"}";}`)
	s := Session{Cookies: []SavedCookie{{Name: "current_path", Value: signed}, {Name: "city_path", Value: "kazan"}}}
	if got := SessionCity(s); got.Name != "Казань" || got.Slug != "kazan" {
		t.Fatalf("city=%+v", got)
	}
	if err := RequireCity(s, "kazan"); err != nil {
		t.Fatal(err)
	}
	if err := RequireCity(s, "moscow"); err == nil || !strings.Contains(err.Error(), "auth import") {
		t.Fatalf("mismatched city err=%v", err)
	}
	// The unsigned slug alone does not select the city on the server.
	if err := RequireCity(Session{Cookies: s.Cookies[1:]}, "kazan"); err == nil {
		t.Fatal("accepted a session without the signed city cookie")
	}
}

func mustSort(t *testing.T, v string) domain.Sort {
	t.Helper()
	s, err := domain.NewSort(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
