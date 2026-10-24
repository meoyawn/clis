package service

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func parseSearch(doc *goquery.Document, u *url.URL, o SearchOptions, status int) (*SearchResult, error) {
	query := strings.TrimSpace(o.Query)
	heading := textOf(doc.Find("h1").First())
	if !strings.Contains(heading, "Товары по запросу «"+query+"»") {
		return nil, fmt.Errorf("page does not confirm query %q", query)
	}
	r := &SearchResult{Query: query, Page: o.Page, Results: []Product{}}
	filters := []Filter{}
	sorting := []Option{}
	categories := &CategoryTree{Path: []Category{}, Children: []Category{}}
	if o.IncludeFilters {
		r.Filters = &filters
		r.Sorting = &sorting
		r.Categories = categories
	}
	empty := strings.Contains(heading, "не найдены") || strings.Contains(textOf(doc.Find(".alert.alert-danger")), "По запросу ничего не найдено")
	if empty {
		if o.Page > 1 {
			return nil, fmt.Errorf("page %d is empty or out of range; restart from page 1", o.Page)
		}
		r.Message = "По запросу ничего не найдено; рекомендации сайта исключены"
		return r, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d did not contain a recognized empty-search response", status)
	}
	total := count(textOf(doc.Find(".quantity-collection__number-collection").First()))
	grid := doc.Find(".js-widget-name-search_productslist").First()
	if total == nil || grid.Length() != 1 {
		return nil, fmt.Errorf("search total or product grid is missing; refusing an ambiguous response")
	}
	r.Total = *total
	var parseErr error
	seen := map[string]bool{}
	grid.Find(".products-list > .products-list__item").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		link := s.Find(".products-list__content-title a").First()
		p := productFacts(s)
		p.URL = absolute(u, link.AttrOr("href", ""))
		p.ID, parseErr = productID(p.URL)
		if parseErr != nil {
			return false
		}
		p.Name = textOf(link)
		if p.Name == "" {
			parseErr = fmt.Errorf("product %s has no name", p.ID)
			return false
		}
		if seen[p.ID] {
			parseErr = fmt.Errorf("duplicate product %s in search grid", p.ID)
			return false
		}
		seen[p.ID] = true
		if image := s.Find(".products-list__image img").First(); image.Length() > 0 {
			raw := image.AttrOr("data-src", image.AttrOr("src", ""))
			if src := absolute(u, raw); src != "" {
				p.Images = append(p.Images, src)
			}
		}
		if category := s.Find(".products-group_link a").First(); category.Length() > 0 {
			p.Category = &Link{Title: textOf(category), URL: absolute(u, category.AttrOr("href", ""))}
		}
		r.Results = append(r.Results, p)
		return true
	})
	if parseErr != nil {
		return nil, parseErr
	}
	r.Count = len(r.Results)
	if r.Count == 0 && r.Total > 0 {
		return nil, fmt.Errorf("site reports %d matches but returned no recognized product cards", r.Total)
	}
	if r.Count > r.Total {
		return nil, fmt.Errorf("product count exceeds the reported search total")
	}
	form := doc.Find(".js-filter-form").First()
	if form.Length() == 0 {
		return nil, fmt.Errorf("search filter form is missing")
	}
	filters = parseFilters(form)
	categories = parseCategories(doc, u)
	sorting = parseSorting(doc, u)
	if err := verifySelection(doc, form, u, o, filters, sorting, categories); err != nil {
		return nil, err
	}
	if o.IncludeFilters {
		r.Filters = &filters
		r.Sorting = &sorting
		r.Categories = categories
	}
	page := 1
	if active := textOf(doc.Find(".pagination .active").First()); active != "" {
		p, err := strconv.Atoi(active)
		if err != nil || p < 1 {
			return nil, fmt.Errorf("invalid current page %q", active)
		}
		page = p
	}
	if page != o.Page {
		return nil, fmt.Errorf("requested page %d but site returned page %d", o.Page, page)
	}
	// The website publishes only a window of page links. Its last visible page
	// number is not necessarily the total page count; use the next-page link.
	doc.Find(".pagination a").Each(func(_ int, a *goquery.Selection) {
		if parseErr != nil || a.HasClass("disabled") {
			return
		}
		next, err := url.Parse(a.AttrOr("href", ""))
		if err != nil || next.Path != "/catalog/search" {
			return
		}
		n, err := strconv.Atoi(next.Query().Get("page"))
		if err != nil || n != o.Page {
			return
		} // Website uses zero-based page offsets.
		resolved := u.ResolveReference(next)
		if resolved.Scheme != u.Scheme || resolved.Host != u.Host || resolved.User != nil {
			parseErr = fmt.Errorf("pagination points outside the catalog origin")
			return
		}
		want, got := u.Query(), next.Query()
		for k, vs := range want {
			if k == "page" || k == "noredirect" {
				continue
			}
			if k == "sort" && vs[0] == sortPopular && !got.Has(k) {
				continue
			}
			if !sameValues(vs, got[k]) {
				parseErr = fmt.Errorf("pagination changed requested parameter %q", k)
				return
			}
		}
		for k := range got {
			if k != "page" && k != "noredirect" && !want.Has(k) {
				parseErr = fmt.Errorf("pagination added unexpected parameter %q", k)
				return
			}
		}
		// Keep the caller's complete selection, including noredirect, for repeatable
		// continuation rather than inheriting transient website navigation state.
		values := u.Query()
		values.Set("page", strconv.Itoa(o.Page))
		target := *u
		target.RawQuery = values.Encode()
		pageNext := o.Page + 1
		urlNext := target.String()
		r.HasMore = true
		r.NextPage = &pageNext
		r.NextURL = &urlNext
	})
	if parseErr != nil {
		return nil, parseErr
	}
	return r, nil
}

func sameValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
	}
	for _, n := range counts {
		if n != 0 {
			return false
		}
	}
	return true
}

func parseFilters(form *goquery.Selection) []Filter {
	filters := []Filter{}
	form.Find(".facets-list__item").Each(func(_ int, g *goquery.Selection) {
		f := Filter{Title: textOf(g.Find(".facets-list__item-title").First())}
		from := g.Find(`input[name$="[from]"]`).First()
		to := g.Find(`input[name$="[to]"]`).First()
		if from.Length() > 0 && to.Length() > 0 {
			f.Type = "range"
			f.FromKey = from.AttrOr("name", "")
			f.ToKey = to.AttrOr("name", "")
			f.Key = strings.TrimSuffix(f.FromKey, "[from]")
			f.Min = number(from.AttrOr("data-value-limit", ""))
			f.Max = number(to.AttrOr("data-value-limit", ""))
			f.From = number(from.AttrOr("value", ""))
			f.To = number(to.AttrOr("value", ""))
			filters = append(filters, f)
			return
		}
		f.Type = "select"
		f.Multiple = true
		g.Find(`input[type="checkbox"]`).Not(".box-field-mobile").Each(func(_ int, input *goquery.Selection) {
			key := input.AttrOr("name", "")
			if key == "" {
				return
			}
			f.Key = key
			value := input.AttrOr("value", "")
			box := input.Parent()
			label := box.Find("label").First().Clone()
			quantity := count(textOf(label.Find(".box-quantity")))
			label.Find(".box-quantity").Remove()
			labelText := textOf(label)
			if labelText == "" {
				labelText = value
			}
			_, selected := input.Attr("checked")
			f.Options = append(f.Options, Option{Value: value, Label: labelText, Count: quantity, Selected: selected})
		})
		if f.Key != "" {
			filters = append(filters, f)
		}
	})
	return filters
}

func parseCategories(doc *goquery.Document, u *url.URL) *CategoryTree {
	tree := &CategoryTree{Path: []Category{}, Children: []Category{}}
	root := doc.Find(".search-categories").First()
	parentID := ""
	root.Find(".active-up, .subcategories-title.active").Each(func(_ int, s *goquery.Selection) {
		selected := s.HasClass("active")
		link := absolute(u, s.AttrOr("href", ""))
		id := ""
		if selected {
			id = u.Query().Get("category_id")
			link = u.String()
		} else if parsed, err := url.Parse(link); err == nil {
			id = parsed.Query().Get("category_id")
		}
		if id == "" {
			return
		}
		title, count := countedLabel(textOf(s))
		tree.Path = append(tree.Path, Category{ID: id, Title: title, Count: count, URL: link, ParentID: parentID, Selected: selected})
		parentID = id
	})
	root.Find(".subcategories-title-default, .subcategories-content__item").Each(func(_ int, s *goquery.Selection) {
		link := absolute(u, s.AttrOr("href", ""))
		parsed, err := url.Parse(link)
		if err != nil {
			return
		}
		id := parsed.Query().Get("category_id")
		if id == "" {
			return
		}
		title, count := countedLabel(textOf(s))
		tree.Children = append(tree.Children, Category{ID: id, Title: title, Count: count, URL: link, ParentID: parentID})
	})
	return tree
}

func parseSorting(doc *goquery.Document, u *url.URL) []Option {
	options := []Option{}
	doc.Find(".js-catalog-sort").First().Find("option").Each(func(i int, s *goquery.Selection) {
		link := absolute(u, s.AttrOr("value", ""))
		parsed, err := url.Parse(link)
		if err != nil {
			return
		}
		_, selected := s.Attr("selected")
		// An HTML select with no selected attribute defaults to its first option.
		if i == 0 && s.Parent().Find("option[selected]").Length() == 0 {
			selected = true
		}
		options = append(options, Option{Value: parsed.Query().Get("sort"), Label: textOf(s), Selected: selected, URL: link})
	})
	return options
}

func verifySelection(doc *goquery.Document, form *goquery.Selection, u *url.URL, o SearchOptions, filters []Filter, sorting []Option, tree *CategoryTree) error {
	query := form.Find(`input[name="q"]`).First().AttrOr("value", "")
	if query != strings.TrimSpace(o.Query) {
		return fmt.Errorf("filter form changed query to %q", query)
	}
	for key, values := range o.Filters {
		if bareFieldPattern.MatchString(key) {
			key += "[]"
		}
		switch key {
		case "category_id":
			if form.Find(`input[name="category_id"]`).First().AttrOr("value", "") != values[0] || len(tree.Path) == 0 || tree.Path[len(tree.Path)-1].ID != values[0] {
				return fmt.Errorf("site did not apply category_id=%s", values[0])
			}
			continue
		case "in_stock":
			// The site's switch uses 0 for hiding preorder products and 1 for
			// showing them. Validate the opposite value on its toggle URL.
			toggle := doc.Find(".js-in-stock-checkbox").First()
			link, err := url.Parse(toggle.AttrOr("data-href", ""))
			if err != nil || toggle.Length() == 0 || link.Query().Get("in_stock") == values[0] || link.Query().Get("in_stock") == "" {
				return fmt.Errorf("site did not confirm in_stock=%s", values[0])
			}
			continue
		}
		confirmed := false
		for _, f := range filters {
			if f.Key == key && f.Type == "select" {
				selected := []string{}
				for _, v := range f.Options {
					if v.Selected {
						selected = append(selected, v.Value)
					}
				}
				confirmed = sameValues(values, selected)
			}
			if key == f.FromKey || key == f.ToKey {
				got := f.From
				if key == f.ToKey {
					got = f.To
				}
				want := number(values[0])
				confirmed = want != nil && got != nil && *want == *got
			}
		}
		if !confirmed {
			return fmt.Errorf("site did not apply filter %s=%s; get current filters with --include-filters", key, strings.Join(values, ","))
		}
	}
	if o.Sort.String() != "" {
		for _, s := range sorting {
			if s.Selected && s.Value == o.Sort.String() {
				return nil
			}
		}
		return fmt.Errorf("site did not apply sort %q", o.Sort.String())
	}
	return nil
}
