package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	analysesValue  = "analyses"
	complexesValue = "complexes"
	checkupsValue  = "checkups"
)

type ProductKind struct{ value string }

func Analyses() ProductKind  { return ProductKind{analysesValue} }
func Complexes() ProductKind { return ProductKind{complexesValue} }
func Checkups() ProductKind  { return ProductKind{checkupsValue} }
func NewProductKindFromValue(value string) (ProductKind, error) {
	switch value {
	case analysesValue, complexesValue, checkupsValue:
		return ProductKind{value}, nil
	default:
		return ProductKind{}, fmt.Errorf("invalid type %q: expected analyses, complexes, or checkups", value)
	}
}
func (k ProductKind) apiPath() string {
	if k == Analyses() {
		return "tests"
	}
	return k.value
}
func (k ProductKind) webPath() string {
	switch k.value {
	case analysesValue:
		return "for-doctors"
	case complexesValue:
		return "profi"
	default:
		return "checkups"
	}
}

var citySlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var validID = regexp.MustCompile(`^[1-9][0-9]*$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type optionalBool struct{ value, set bool }

func (b *optionalBool) String() string   { return strconv.FormatBool(b.value) }
func (b *optionalBool) IsBoolFlag() bool { return true }
func (b *optionalBool) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("invalid boolean %q: expected true or false", s)
	}
	b.value, b.set = v, true
	return nil
}

type searchFilters struct {
	AtHome, Promo optionalBool
	Deadline      int
}

func (f searchFilters) values() url.Values {
	v := url.Values{}
	if f.AtHome.set {
		v.Set("available_at_home", f.AtHome.String())
	}
	if f.Promo.set {
		v.Set("is_promo", f.Promo.String())
	}
	if f.Deadline >= 0 {
		v.Set("deadline", strconv.Itoa(f.Deadline))
	}
	return v
}

type productRef struct {
	ID, City string
	Kind     ProductKind
}

func parseReference(value string) (productRef, error) {
	if len(value) <= 64 && (validID.MatchString(value) || uuidPattern.MatchString(value)) {
		return productRef{ID: value}, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || (u.Host != "www.invitro.ru" && u.Host != "invitro.ru") || u.User != nil || u.RawPath != "" {
		return productRef{}, fmt.Errorf("invalid product reference %.200q: expected a numeric website ID, UUID, or https://www.invitro.ru/analizes/ product URL", value)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "analizes" {
		return productRef{}, fmt.Errorf("invalid product URL %.200q: expected /analizes/<type>/[city/]<category>/<id>/", value)
	}
	kind := Analyses()
	switch parts[1] {
	case "for-doctors":
	case "profi":
		kind = Complexes()
	case "checkups":
		kind = Checkups()
	default:
		return productRef{}, fmt.Errorf("unsupported catalog URL type %q", parts[1])
	}
	city, tail := "moscow", parts[2:]
	if len(tail) == 3 {
		city, tail = tail[0], tail[1:]
	}
	if err := validateCity(city); err != nil {
		return productRef{}, err
	}
	if len(tail) != 2 || !(validID.MatchString(tail[0]) || (kind == Checkups() && tail[0] == "programmy-zdorovya")) ||
		(!validID.MatchString(tail[1]) && !(kind == Checkups() && uuidPattern.MatchString(tail[1]))) {
		return productRef{}, fmt.Errorf("invalid product URL %.200q: expected a category and product ID", value)
	}
	return productRef{ID: tail[1], City: city, Kind: kind}, nil
}

type cityInfo struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}
type category struct {
	ID       string `json:"id"`
	BitrixID int64  `json:"bitrix_id"`
	Title    string `json:"title"`
}
type service struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Price *float64 `json:"price"`
}
type apiProduct struct {
	ID                  string            `json:"id"`
	BitrixID            int64             `json:"bitrix_id"`
	CategoryID          int64             `json:"category_bitrix_id"`
	Title               string            `json:"title"`
	Code                string            `json:"code"`
	Article             string            `json:"article"`
	Price               *float64          `json:"price"`
	OldPrice            *float64          `json:"old_price"`
	LoyaltyBonus        *float64          `json:"loyalty_bonus"`
	Deadline            *int              `json:"deadline"`
	WeekdaysOnly        *bool             `json:"is_weekdays_only"`
	AtHome              *bool             `json:"available_at_home"`
	Promo               *bool             `json:"is_promo"`
	Subtitle            string            `json:"subtitle"`
	Description         string            `json:"description"`
	AnnounceDescription string            `json:"announce_description"`
	Material            string            `json:"material"`
	Method              string            `json:"text_method"`
	Services            []service         `json:"additional_services"`
	Categories          []category        `json:"categories"`
	Tests               []json.RawMessage `json:"tests"`
	IncludedServices    []json.RawMessage `json:"services"`
	Tags                []string          `json:"tags"`
}

type product struct {
	ID                 string            `json:"id"`
	CatalogID          string            `json:"catalog_id"`
	Type               string            `json:"type"`
	URL                string            `json:"url"`
	Name               string            `json:"name"`
	Code               string            `json:"code,omitempty"`
	Description        string            `json:"description,omitempty"`
	PriceFrom          *float64          `json:"price_from"`
	OldPrice           *float64          `json:"old_price,omitempty"`
	LoyaltyBonus       *float64          `json:"loyalty_bonus"`
	Currency           string            `json:"currency"`
	AdditionalServices []service         `json:"additional_services"`
	Deadline           *int              `json:"deadline_days"`
	WeekdaysOnly       *bool             `json:"is_weekdays_only"`
	DeadlineNote       string            `json:"deadline_note,omitempty"`
	AtHome             *bool             `json:"available_at_home"`
	Promo              *bool             `json:"is_promo"`
	Material           string            `json:"material,omitempty"`
	Method             string            `json:"method,omitempty"`
	Categories         []category        `json:"categories,omitempty"`
	Tests              []json.RawMessage `json:"tests,omitempty"`
	IncludedServices   []json.RawMessage `json:"services,omitempty"`
	Tags               []string          `json:"tags,omitempty"`
}

func normalizeProduct(p apiProduct, city string, kind ProductKind) (product, error) {
	if !uuidPattern.MatchString(p.ID) || p.Title == "" || (kind != Checkups() && p.BitrixID <= 0) {
		return product{}, fmt.Errorf("invalid catalog product identity %q (website ID %d) or missing title", p.ID, p.BitrixID)
	}
	id, categoryID := strconv.FormatInt(p.BitrixID, 10), strconv.FormatInt(p.CategoryID, 10)
	if p.CategoryID <= 0 && len(p.Categories) > 0 {
		categoryID = strconv.FormatInt(p.Categories[len(p.Categories)-1].BitrixID, 10)
	}
	if kind == Checkups() {
		id, categoryID = p.ID, "programmy-zdorovya"
	}
	if categoryID == "0" {
		return product{}, fmt.Errorf("missing category for product %q", id)
	}
	cityPath := city + "/"
	if city == "moscow" {
		cityPath = ""
	}
	item := productFacts(p, kind)
	item.ID = id
	item.URL = siteOrigin + "/analizes/" + kind.webPath() + "/" + cityPath + categoryID + "/" + id + "/"
	return item, nil
}

// Shared JSON facts for catalog products and promotion-only articles. A caller
// supplies the public URL; some promotion articles have no website ID/category.
func productFacts(p apiProduct, kind ProductKind) product {
	if p.Services == nil {
		p.Services = []service{}
	}
	description := p.Subtitle
	if description == "" {
		description = p.Description
	}
	if description == "" {
		description = p.AnnounceDescription
	}
	deadlineNote := ""
	if p.Deadline != nil {
		deadlineNote = "День сдачи биоматериала в срок не входит"
	}
	code := p.Code
	if code == "" {
		code = p.Article
	}
	return product{
		ID: p.ID, CatalogID: p.ID, Type: kind.value,
		Name: p.Title, Code: code, Description: plainText(description), PriceFrom: p.Price, OldPrice: p.OldPrice, Currency: "RUB",
		LoyaltyBonus:       p.LoyaltyBonus,
		AdditionalServices: p.Services, Deadline: p.Deadline, WeekdaysOnly: p.WeekdaysOnly,
		DeadlineNote: deadlineNote, AtHome: p.AtHome, Promo: p.Promo,
		Material: plainText(p.Material), Method: plainText(p.Method), Categories: p.Categories, Tests: p.Tests,
		IncludedServices: p.IncludedServices, Tags: p.Tags,
	}
}

type filterDefinition struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	IsActive bool   `json:"is_active"`
	Config   string `json:"config"`
	CLIFlag  string `json:"cli_flag,omitempty"`
}

type filterCatalog map[string]filterDefinition

type searchResult struct {
	Query            string            `json:"query"`
	City             string            `json:"city"`
	CityID           string            `json:"city_id"`
	Type             string            `json:"type"`
	Filters          map[string]string `json:"filters"`
	AvailableFilters *filterCatalog    `json:"available_filters,omitempty"`
	Total            int               `json:"total"`
	Count            int               `json:"count"`
	Page             int               `json:"page"`
	TotalPages       int               `json:"total_pages"`
	HasMore          bool              `json:"has_more"`
	NextPage         *int              `json:"next_page"`
	NextURL          string            `json:"next_url,omitempty"`
	Results          []product         `json:"results"`
	SourceURL        string            `json:"source_url"`
	FetchedAt        time.Time         `json:"fetched_at"`
	HTTPRequests     int               `json:"http_requests"`
}
type detailResult struct {
	product
	City         string            `json:"city"`
	CityID       string            `json:"city_id"`
	Sections     map[string]string `json:"sections,omitempty"`
	Source       json.RawMessage   `json:"source,omitempty"`
	SourceURL    string            `json:"source_url"`
	FetchedAt    time.Time         `json:"fetched_at"`
	HTTPRequests int               `json:"http_requests"`
}

// Preserve paragraph, list, and table boundaries while removing source markup.
func plainText(markup string) string {
	root, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return strings.TrimSpace(markup)
	}
	var out strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		block := n.Type == html.ElementNode && strings.Contains("|p|div|li|ul|ol|tr|h1|h2|h3|h4|table|blockquote|", "|"+n.Data+"|")
		if block {
			out.WriteByte('\n')
		}
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "br" {
			out.WriteByte('\n')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
		if block {
			out.WriteByte('\n')
		}
		if n.Type == html.ElementNode && (n.Data == "td" || n.Data == "th") {
			out.WriteString(" | ")
		}
	}
	visit(root)
	var lines []string
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(strings.Join(strings.Fields(line), " "))
		line = strings.TrimSuffix(line, " |")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
