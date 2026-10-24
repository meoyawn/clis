package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Show fetches one product page. Full mode also fetches its details section and
// the first cheaper/faster offer lists, for at most four pages. Each page may
// additionally follow up to three validated redirects.
func (c *Client) Show(ctx context.Context, ref ItemReference, o ShowOptions, opt RequestOptions) (*ShowResult, error) {
	if !skuPattern.MatchString(ref.SKU) {
		return nil, fmt.Errorf("invalid SKU %q", ref.SKU)
	}
	p, err := c.fetchProduct(ctx, ref.Path, ref.SKU, opt)
	if err != nil {
		return nil, err
	}
	out, err := parseShow(p, ref)
	if err != nil {
		return nil, err
	}
	out.Full = o.Full
	if !o.Full {
		return out, nil
	}
	paginators, err := p.widgets("paginator")
	if err != nil {
		return nil, err
	}
	detailPath := ""
	for _, pg := range paginators {
		if str(pg["layoutContainer"]) == "pdpPage2column" {
			detailPath = str(pg["nextPage"])
			break
		}
	}
	if detailPath != "" {
		if err := validateProductSection(detailPath, ref.SKU); err != nil {
			return nil, err
		}
		detail, err := c.fetchProduct(ctx, detailPath, ref.SKU, opt)
		if err != nil {
			return nil, fmt.Errorf("load product description and characteristics: %w", err)
		}
		mergeMeta(&out.Metadata, detail.meta)
		if err := parseDetails(detail, out); err != nil {
			return nil, err
		}
	}
	best, err := p.first("webBestSeller")
	if err != nil {
		return nil, err
	}
	if link := str(best["modalLink"]); link != "" {
		safe, err := safePageURL(link)
		if err != nil {
			return nil, err
		}
		u, err := url.Parse(safe)
		if err != nil {
			return nil, err
		}
		if u.Path != "/modal/otherOffersFromSellers" || u.Query().Get("product_id") != ref.SKU {
			return nil, fmt.Errorf("seller overlay does not belong to SKU %s", ref.SKU)
		}
		// These two fixed views are the same choices the website exposes.
		for _, view := range [...]SellerView{CheaperSellerView(), FasterSellerView()} {
			q := u.Query()
			q.Set("sort", view.String())
			u.RawQuery = q.Encode()
			sellers, err := c.fetchProduct(ctx, u.String(), ref.SKU, opt)
			if err != nil {
				return nil, fmt.Errorf("load %s seller offers: %w", view.String(), err)
			}
			mergeMeta(&out.Metadata, sellers.meta)
			if err := parseSellers(sellers, out, view); err != nil {
				return nil, err
			}
		}
	}
	if out.Description == "" {
		out.Warnings = append(out.Warnings, "Ozon did not supply a text description in the fetched product sections.")
	}
	return out, nil
}

func validateProductSection(path, sku string) error {
	safe, err := safePageURL(path)
	if err != nil {
		return err
	}
	u, err := url.Parse(safe)
	if err != nil {
		return err
	}
	m := productPattern.FindStringSubmatch(u.Path)
	if m == nil || m[1] != sku || u.Query().Get("layout_container") != "pdpPage2column" {
		return fmt.Errorf("details section does not belong to SKU %s", sku)
	}
	return nil
}

func parseShow(p *page, ref ItemReference) (*ShowResult, error) {
	main, err := p.first("webProductMainWidget")
	if err != nil {
		return nil, err
	}
	gallery, err := p.first("webGallery")
	if err != nil {
		return nil, err
	}
	sku := str(main["sku"])
	if sku == "" {
		sku = str(gallery["sku"])
	}
	if sku != ref.SKU {
		return nil, fmt.Errorf("Ozon returned product SKU %q, requested %s", sku, ref.SKU)
	}
	heading, err := p.first("webProductHeading")
	if err != nil {
		return nil, err
	}
	out := &ShowResult{Metadata: p.meta, Product: Product{SKU: sku, URL: publicURL(str(main["url"])), Title: str(heading["title"]), Images: []string{}}, Variants: []VariantGroup{}, Sellers: []Seller{}, Characteristics: []Attribute{}, DescriptionImages: []string{}}
	if out.Title == "" {
		return nil, fmt.Errorf("Ozon product %s has no title", sku)
	}
	if out.URL == "" {
		out.URL = WebOrigin + ref.Path
	}
	out.Images = addUnique(out.Images, publicURL(str(gallery["coverImage"])))
	for _, im := range arr(gallery["images"]) {
		out.Images = addUnique(out.Images, publicURL(str(at(im, "src"))))
	}
	prices, err := p.first("webPrice")
	if err != nil {
		return nil, err
	}
	out.Prices = Prices{WithOzonCard: money(prices["cardPrice"]), WithoutOzonCard: money(prices["price"]), Original: money(prices["originalPrice"]), CardLabel: str(at(prices, "lexemes", "withOzonCard")), RegularLabel: str(at(prices, "lexemes", "withoutOzonCard"))}
	out.Price = out.Prices.WithOzonCard
	if out.Price == nil {
		out.Price = out.Prices.WithoutOzonCard
	}
	out.Available = boolean(prices["isAvailable"])
	score, err := p.first("webReviewProductScore")
	if err != nil {
		return nil, err
	}
	out.Rating = number(score["totalScore"])
	out.NumRatings = integer(score["reviewsCount"])
	cart, err := p.first("webAddToCart")
	if err != nil {
		return nil, err
	}
	out.Quantity = integer(at(cart, "firstButton", "freeRest"))
	if out.Quantity != nil {
		out.QuantitySource = "freeRest"
	}
	out.DeliveryDay = text(at(cart, "firstButton", "toCart", "subtitle"))
	aspects, err := p.first("webAspects")
	if err != nil {
		return nil, err
	}
	for _, a := range arr(aspects["aspects"]) {
		group := VariantGroup{Key: str(at(a, "aspectKey")), Name: str(at(a, "aspectName")), Variants: []Variant{}}
		for _, v := range arr(at(a, "variants")) {
			d := at(v, "data")
			selected := boolValue(at(v, "active"))
			value := str(at(d, "searchableText"))
			if value == "" {
				value = text(at(d, "textRs"))
			}
			variant := Variant{SKU: str(at(v, "sku")), URL: publicURL(str(at(v, "link"))), Title: str(at(d, "title")), Value: value, Selected: selected, Availability: str(at(v, "availability")), Image: publicURL(str(at(d, "coverImage"))), Price: money(at(d, "price"))}
			if variant.Image == "" {
				variant.Image = publicURL(str(at(d, "picture")))
			}
			group.Variants = append(group.Variants, variant)
		}
		out.Variants = append(out.Variants, group)
	}
	current, err := p.first("webCurrentSeller")
	if err != nil {
		return nil, err
	}
	if current != nil {
		id := str(at(current, "header", "badge", "unsubscribed", "common", "action", "params", "sellerId"))
		out.Sellers = append(out.Sellers, Seller{ID: id, Name: text(at(current, "sellerCell", "centerBlock", "title")), URL: publicURL(str(at(current, "sellerCell", "common", "action", "link"))), SKU: sku, ProductURL: out.URL, Prices: out.Prices, DeliveryDay: out.DeliveryDay, Rating: textNumber(text(at(current, "rating", "title"))), Current: true, SeenIn: []string{"current"}})
	}
	if err := parseDetails(p, out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseDetails(p *page, out *ShowResult) error {
	characteristics, err := p.widgets("webCharacteristics")
	if err != nil {
		return err
	}
	for _, w := range characteristics {
		for _, g := range arr(w["characteristics"]) {
			for _, key := range []string{"short", "long"} {
				for _, c := range arr(at(g, key)) {
					a := Attribute{Key: str(at(c, "key")), Name: str(at(c, "name")), Group: text(at(g, "title")), Values: []string{}}
					for _, v := range arr(at(c, "values")) {
						a.Values = append(a.Values, text(v))
					}
					if a.Name != "" {
						out.Characteristics = append(out.Characteristics, a)
					}
				}
			}
		}
	}
	descriptions, err := p.widgets("webDescription")
	if err != nil {
		return err
	}
	var parts []string
	for _, w := range descriptions {
		for _, c := range arr(w["characteristics"]) {
			s := text(at(c, "content"))
			if s != "" {
				parts = append(parts, strings.TrimSpace(text(at(c, "title"))+"\n"+s))
			}
		}
		for _, key := range []string{"description", "text", "annotation"} {
			if s := text(w[key]); s != "" {
				parts = append(parts, s)
			}
		}
		rich := w["richAnnotationJson"]
		if s, ok := rich.(string); ok {
			if json.Unmarshal([]byte(s), &rich) != nil {
				return fmt.Errorf("invalid product rich description JSON")
			}
		}
		richDescription(rich, &parts, &out.DescriptionImages)
	}
	if len(parts) > 0 {
		if out.Description != "" {
			parts = append([]string{out.Description}, parts...)
		}
		out.Description = strings.Join(parts, "\n\n")
	}
	return nil
}

// Only content-bearing keys are traversed. Layout parameters, action URLs and
// tracking data never become a product's description.
func richDescription(x any, parts, images *[]string) {
	switch v := x.(type) {
	case []any:
		for _, item := range v {
			richDescription(item, parts, images)
		}
	case map[string]any:
		for _, key := range []string{"title", "text"} {
			if s := text(v[key]); s != "" {
				*parts = append(*parts, s)
			}
		}
		*images = addUnique(*images, publicURL(str(at(v, "img", "src"))))
		for _, key := range []string{"content", "blocks", "items", "columns"} {
			richDescription(v[key], parts, images)
		}
	}
}

func parseSellers(p *page, out *ShowResult, view SellerView) error {
	w, err := p.first("webSellerList")
	if err != nil {
		return err
	}
	if w == nil {
		return fmt.Errorf("Ozon seller overlay has no seller list")
	}
	raw, ok := w["sellers"].([]any)
	if !ok {
		return fmt.Errorf("Ozon seller overlay has no sellers array")
	}
	hidden := boolValue(w["isMoreButtonHidden"])
	if !hidden {
		out.SellersTruncated = true
	}
	for index, v := range raw {
		s := Seller{ID: str(at(v, "id")), Name: str(at(v, "name")), URL: publicURL(str(at(v, "link"))), SKU: str(at(v, "sku")), ProductURL: publicURL(str(at(v, "productLink"))), SeenIn: []string{view.String()}}
		rank := index + 1
		if view.value == cheaperSellerValue {
			s.PriceRank = &rank
		} else {
			s.DeliveryRank = &rank
		}
		s.Prices = Prices{WithOzonCard: money(at(v, "price", "cardPrice", "price")), WithoutOzonCard: money(at(v, "price", "price")), Original: money(at(v, "price", "originalPrice"))}
		for _, a := range arr(at(v, "advantages")) {
			if str(at(a, "key")) == "delivery" {
				s.DeliveryDay = text(at(a, "contentRs", "headRs"))
			}
		}
		s.Rating = number(at(v, "rating"))
		if s.ID == "" || s.SKU == "" {
			return fmt.Errorf("Ozon returned a seller offer without ID or SKU")
		}
		found := false
		for i, old := range out.Sellers {
			if old.ID == s.ID && old.SKU == s.SKU {
				out.Sellers[i].SeenIn = addUnique(old.SeenIn, view.String())
				if s.PriceRank != nil {
					out.Sellers[i].PriceRank = s.PriceRank
				}
				if s.DeliveryRank != nil {
					out.Sellers[i].DeliveryRank = s.DeliveryRank
				}
				if old.DeliveryDay == "" {
					out.Sellers[i].DeliveryDay = s.DeliveryDay
				}
				found = true
				break
			}
		}
		if !found {
			out.Sellers = append(out.Sellers, s)
		}
	}
	return nil
}
