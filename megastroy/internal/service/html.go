package service

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

var numberPattern = regexp.MustCompile(`^[+-]?[0-9]+(?:[.,][0-9]+)?$`)
var countPattern = regexp.MustCompile(`\(([0-9 ]+)\)\s*$`)
var leadingNumber = regexp.MustCompile(`^([0-9][0-9\s\x{00a0}\x{202f}]*(?:[.,][0-9]+)?)`)
var productIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func textOf(s *goquery.Selection) string { return clean(s.Text()) }

func number(s string) *float64 {
	s = strings.Join(strings.Fields(s), "")
	if !numberPattern.MatchString(s) {
		return nil
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil {
		return nil
	}
	return &n
}

func count(s string) *int {
	m := leadingNumber.FindString(clean(s))
	n := number(m)
	if n == nil || *n != float64(int(*n)) {
		return nil
	}
	i := int(*n)
	return &i
}

func countedLabel(s string) (string, *int) {
	s = clean(s)
	m := countPattern.FindStringSubmatch(s)
	if m == nil {
		return s, nil
	}
	return strings.TrimSpace(strings.TrimSuffix(s, m[0])), count(m[1])
}

func absolute(base *url.URL, raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || raw == "" {
		return ""
	}
	u = base.ResolveReference(u)
	if u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.String()
}

func price(s *goquery.Selection) *Money {
	if s.Length() == 0 {
		return nil
	}
	text := textOf(s)
	raw := s.Find(`[itemprop="price"]`).First().AttrOr("content", "")
	if raw == "" {
		raw = leadingNumber.FindString(text)
	}
	n := number(raw)
	if n == nil {
		return nil
	}
	currency := s.Find(`[itemprop="priceCurrency"]`).First().AttrOr("content", "")
	if currency == "" && strings.Contains(text, "₽") {
		currency = "RUB"
	}
	if currency == "" {
		return nil
	}
	return &Money{Amount: *n, Currency: currency, Text: text}
}

func unit(text string) string {
	_, after, ok := strings.Cut(text, "/")
	if !ok {
		return ""
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func parseStock(address, text string) Stock {
	s := Stock{Address: clean(address), Text: clean(text)}
	m := leadingNumber.FindString(s.Text)
	s.Quantity = number(m)
	if s.Quantity != nil {
		s.Unit = strings.TrimSpace(strings.TrimPrefix(s.Text, m))
	}
	return s
}

func productFacts(root *goquery.Selection) Product {
	p := Product{Images: []string{}, Stores: []Stock{}}
	// The page publishes filled stars, not an exact decimal average. Restrict
	// this to the card/title rating so the review form's default five stars and
	// individual customer reviews cannot become the product's rating.
	rating := root.Find(".products-list__content > .rating, .product-content > .rating").First()
	reviews := textOf(rating.Find(".rating-quantity").First())
	if strings.HasPrefix(reviews, "(") {
		_, p.ReviewCount = countedLabel(reviews)
	} else {
		p.ReviewCount = count(reviews)
	}
	stars := rating.Find(".rating-star > .rating-star__item")
	filled := stars.Filter(".active").Length()
	if stars.Length() == 5 && filled > 0 && (p.ReviewCount == nil || *p.ReviewCount > 0) {
		n := float64(filled)
		p.Rating = &n
	}
	current := root.Find(".products-price__value").First()
	p.Price = price(current)
	p.OldPrice = price(root.Find(".products-price__crossed").First())
	p.PriceLabel = textOf(root.Find(".final-price").First())
	p.Unit = unit(textOf(current))
	status := root.Find(`[itemprop="availability"]`).First().AttrOr("href", "")
	if status != "" {
		switch strings.TrimSuffix(status, "/") {
		case "http://schema.org/InStock", "https://schema.org/InStock":
			v := true
			p.Available = &v
		case "http://schema.org/OutOfStock", "https://schema.org/OutOfStock":
			v := false
			p.Available = &v
		}
	}
	p.Availability = textOf(root.Find(".quantity-notice").First())
	button := root.Find(".products-list__content-button button, #product-add-to-cart-button > button").First()
	if label := textOf(button); label != "" {
		if p.Availability != "" {
			p.Availability += "; "
		}
		p.Availability += label
		if title := button.AttrOr("title", ""); title != "" {
			p.Availability += "; " + clean(title)
		}
	}
	root.Find(".store-addresses__item").Each(func(_ int, s *goquery.Selection) {
		p.Stores = append(p.Stores, parseStock(textOf(s.Find(".store-location__street")), textOf(s.Find(".store-quantity"))))
	})
	root.Find(".labels li").Each(func(_ int, s *goquery.Selection) {
		if label := textOf(s); label != "" {
			p.Labels = appendUnique(p.Labels, label)
		}
	})
	return p
}

func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

// blockText retains paragraph/list/table boundaries without copying scripts,
// styles, or unrelated widgets into the product description.
func blockText(s *goquery.Selection) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		block := n.Data == "p" || n.Data == "div" || n.Data == "li" || n.Data == "tr" || n.Data == "br" || strings.HasPrefix(n.Data, "h") && len(n.Data) == 2
		if block {
			b.WriteByte('\n')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if block {
			b.WriteByte('\n')
		} else if n.Data == "td" || n.Data == "th" {
			b.WriteByte('\t')
		}
	}
	for _, n := range s.Nodes {
		walk(n)
	}
	var lines []string
	for _, line := range strings.Split(b.String(), "\n") {
		if line = clean(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func productID(raw string) (string, error) {
	if productIDPattern.MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err == nil && u.Scheme == "https" && u.Host == "kazan.megastroy.com" && u.User == nil {
		path := strings.TrimSuffix(u.Path, "/")
		id, found := strings.CutPrefix(path, "/products/")
		if found && productIDPattern.MatchString(id) {
			return id, nil
		}
	}
	return "", fmt.Errorf("invalid product %q: expected a positive product code or %s/products/CODE URL", raw, Origin)
}
