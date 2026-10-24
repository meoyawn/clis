package service

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func parseDetails(doc *goquery.Document, u *url.URL, id string) (*DetailResult, error) {
	product := doc.Find(".product.js-counter-product-page").First()
	if product.AttrOr("data-counter-product-id", "") != id {
		return nil, fmt.Errorf("page does not confirm requested product code %s", id)
	}
	root := product.Find(".product-head").First()
	if root.Length() == 0 {
		return nil, fmt.Errorf("main product section is missing")
	}
	code := textOf(root.Find(".product-code .js-codelink").First())
	if code != id {
		return nil, fmt.Errorf("visible product code %q differs from requested code %s", code, id)
	}
	if canonical := doc.Find(`link[rel="canonical"]`).First().AttrOr("href", ""); canonical != "" {
		canonicalID, err := productID(absolute(u, canonical))
		if err != nil || canonicalID != id {
			return nil, fmt.Errorf("canonical URL does not identify product %s", id)
		}
	}
	r := &DetailResult{Product: productFacts(root), Characteristics: []Characteristic{}, Breadcrumbs: []Link{}, Documents: []Link{}}
	r.ID = id
	r.URL = u.String()
	r.Name = textOf(root.Find("h1").First())
	if r.Name == "" {
		return nil, fmt.Errorf("product name is missing")
	}
	description := root.Find("#content-description").First()
	r.Description = blockText(description.ChildrenFiltered(".collapse-table").Not(".table-responsive"))
	description.Find(".table-responsive .collapse-table__item").Each(func(_ int, row *goquery.Selection) {
		cells := row.ChildrenFiltered(".side")
		if cells.Length() < 2 {
			return
		}
		name := strings.TrimSuffix(textOf(cells.First()), ":")
		value := textOf(cells.Eq(1))
		if name != "" {
			r.Characteristics = append(r.Characteristics, Characteristic{Name: name, Value: value})
		}
	})
	// Some product pages have only the characteristics next to the title. Merge
	// by the actual name/value pair, preserving additional or conflicting facts.
	root.Find(".product-content .table-responsive li").Each(func(_ int, row *goquery.Selection) {
		label := row.ChildrenFiltered("span").First()
		name := strings.TrimSuffix(textOf(label), ":")
		if name == "" {
			return
		}
		copy := row.Clone()
		copy.ChildrenFiltered("span").First().Remove()
		property := Characteristic{Name: name, Value: textOf(copy)}
		for _, existing := range r.Characteristics {
			if existing == property {
				return
			}
		}
		r.Characteristics = append(r.Characteristics, property)
	})
	r.Dimensions = dimensions(r.Characteristics)
	root.Find(".product-slider a.js-fancybox").Each(func(_ int, a *goquery.Selection) {
		if image := absolute(u, a.AttrOr("href", "")); image != "" {
			r.Images = appendUnique(r.Images, image)
		}
	})
	root.Find(".store-list__item").Each(func(_ int, row *goquery.Selection) {
		cells := row.ChildrenFiltered("div")
		if cells.Length() < 2 {
			return
		}
		r.Stores = append(r.Stores, parseStock(textOf(cells.First()), textOf(cells.Eq(1))))
	})
	doc.Find(".crumbs a").Each(func(_ int, a *goquery.Selection) {
		if link := absolute(u, a.AttrOr("href", "")); link != "" {
			crumb := Link{Title: textOf(a), URL: link}
			if !slices.Contains(r.Breadcrumbs, crumb) {
				r.Breadcrumbs = append(r.Breadcrumbs, crumb)
			}
		}
	})
	if len(r.Breadcrumbs) > 0 {
		category := r.Breadcrumbs[len(r.Breadcrumbs)-1]
		r.Category = &category
	}
	description.Find("#product-certificates a[href]").Each(func(_ int, a *goquery.Selection) {
		if link := absolute(u, a.AttrOr("href", "")); link != "" {
			r.Documents = append(r.Documents, Link{Title: textOf(a), URL: link})
		}
	})
	return r, nil
}

var dimensionPattern = regexp.MustCompile(`^(высота|ширина|глубина|длина|диаметр)(?:,\s*(мм|см|м))?$`)
var measurementPattern = regexp.MustCompile(`^([0-9]+(?:[.,][0-9]+)?)\s*(мм|см|м)?$`)

func dimensions(properties []Characteristic) Dimensions {
	d := Dimensions{Properties: []Characteristic{}}
	values := map[string]*float64{}
	conflicts := map[string]bool{}
	for _, p := range properties {
		name := strings.ToLower(p.Name)
		if strings.Contains(name, "высота") || strings.Contains(name, "ширина") || strings.Contains(name, "глубина") || strings.Contains(name, "длина") || strings.Contains(name, "диаметр") || strings.Contains(name, "расстояние") || strings.Contains(name, "размер") || strings.Contains(name, "толщина") {
			d.Properties = append(d.Properties, p)
		}
		nameMatch := dimensionPattern.FindStringSubmatch(name)
		valueMatch := measurementPattern.FindStringSubmatch(strings.ToLower(clean(p.Value)))
		if nameMatch == nil || valueMatch == nil {
			continue
		}
		axis := nameMatch[1]
		unit := nameMatch[2]
		if valueMatch[2] != "" {
			unit = valueMatch[2]
		}
		n := number(valueMatch[1])
		if n == nil || unit == "" {
			continue
		}
		switch unit {
		case "см":
			*n *= 10
		case "м":
			*n *= 1000
		}
		if previous := values[axis]; previous != nil && *previous != *n {
			conflicts[axis] = true
		}
		values[axis] = n
	}
	for axis := range conflicts {
		values[axis] = nil
	}
	d.HeightMM = values["высота"]
	d.WidthMM = values["ширина"]
	d.DepthMM = values["глубина"]
	d.LengthMM = values["длина"]
	d.DiameterMM = values["диаметр"]
	return d
}
