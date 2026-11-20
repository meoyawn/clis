package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"golang.org/x/net/html"
)

// CompanyVacancies reads the employer's public vacancy page through GET only.
// The page renders every card server-side; a populated paginator is an
// unsupported contract and stops rather than returning a partial list.
func (c *Client) CompanyVacancies(ctx context.Context, company domain.CompanyID) (Object, error) {
	path := "/companies/" + company.String() + "/vacancies"
	p, err := c.readPage(ctx, path)
	if err != nil {
		return nil, err
	}
	alias, _ := p.state["companyAlias"].(string)
	companyID := number(p.state["companyId"])
	if alias != company.String() || companyID == "" || p.state["isCompanyVacanciesPage"] != true {
		return nil, fmt.Errorf("company %q vacancy page identity could not be verified; stopped", company.String())
	}
	paginator := find(p.doc, func(n *html.Node) bool { return attr(n, "id") == "paginator" })
	if paginator == nil {
		return nil, fmt.Errorf("company vacancy page has no native paginator; contract changed; stopped")
	}
	if find(paginator, func(n *html.Node) bool { return n.Type == html.ElementNode && n != paginator }) != nil {
		return nil, fmt.Errorf("company vacancy page is paginated; pagination is unsupported; stopped without a partial list")
	}
	rows := []any{}
	seen := map[string]bool{}
	for _, card := range findAll(p.doc, func(n *html.Node) bool { return n.Data == "div" && hasClass(n, "vacancy-card") }) {
		link := find(card, func(n *html.Node) bool { return n.Data == "a" && hasClass(n, "vacancy-card__title-link") })
		var employer *html.Node
		if box := find(card, func(n *html.Node) bool { return hasClass(n, "vacancy-card__company") }); box != nil {
			employer = find(box, func(n *html.Node) bool { return n.Data == "a" && strings.HasPrefix(attr(n, "href"), "/companies/") })
		}
		date := find(card, func(n *html.Node) bool { return n.Data == "time" && attr(n, "datetime") != "" })
		if link == nil || employer == nil || date == nil || nodeText(link) == "" {
			return nil, fmt.Errorf("company vacancy card lacks a role, employer or date; contract changed; stopped")
		}
		id, err := domain.NewVacancyIDFromValue(Origin + attr(link, "href"))
		if err != nil || seen[id.String()] {
			return nil, fmt.Errorf("company vacancy card has a duplicate or invalid role URL %q; stopped", attr(link, "href"))
		}
		if attr(employer, "href") != "/companies/"+company.String() {
			return nil, fmt.Errorf("vacancy %s on company page belongs to %q; stopped", id.String(), attr(employer, "href"))
		}
		published, err := time.Parse(time.RFC3339, attr(date, "datetime"))
		if err != nil {
			return nil, fmt.Errorf("vacancy %s has unsupported date %q; stopped", id.String(), attr(date, "datetime"))
		}
		seen[id.String()] = true
		var skills []any
		if box := find(card, func(n *html.Node) bool { return hasClass(n, "vacancy-card__skills") }); box != nil {
			for _, chip := range findAll(box, func(n *html.Node) bool { return hasClass(n, "basic-chip__text") }) {
				skills = append(skills, nodeText(chip))
			}
		}
		var meta []any
		if box := find(card, func(n *html.Node) bool { return hasClass(n, "vacancy-card__meta") }); box != nil {
			for _, chip := range findAll(box, func(n *html.Node) bool { return hasClass(n, "chip-with-icon__text") || hasClass(n, "basic-chip__text") }) {
				meta = append(meta, nodeText(chip))
			}
		}
		row := Object{"id": id.String(), "url": Origin + "/vacancies/" + id.String(), "title": nodeText(link), "published_at": published.Format(time.RFC3339)}
		if len(meta) > 0 {
			row["meta"] = meta
		}
		if len(skills) > 0 {
			row["skills"] = skills
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 && find(p.doc, func(n *html.Node) bool { return hasClass(n, "no-content__title") }) == nil {
		return nil, fmt.Errorf("company vacancy page has no cards and no native empty state; stopped")
	}
	return Object{"company_alias": alias, "company_id": companyID, "url": Origin + path, "vacancies": rows}, nil
}
