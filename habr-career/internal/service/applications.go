package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"golang.org/x/net/html"
)

var countPattern = regexp.MustCompile(`\(([0-9]+)\)`)

func hasClass(n *html.Node, class string) bool {
	for _, value := range strings.Fields(attr(n, "class")) {
		if value == class {
			return true
		}
	}
	return false
}

func findAll(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	if match(n) {
		out = append(out, n)
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		out = append(out, findAll(child, match)...)
	}
	return out
}

// Applications follows only the site's own pagination links. It never opens
// conversations, quick responses, deleted responses or notification read APIs.
func (c *Client) Applications(ctx context.Context) (Object, error) {
	profile, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	alias := profile["alias"].(string)
	rows := []any{}
	seen := map[string]bool{}
	path, current, total := "/responses", 1, -1
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		body, err := c.request(ctx, http.MethodGet, path, "", nil)
		if err != nil {
			return nil, err
		}
		doc, err := html.Parse(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		identity := find(doc, func(n *html.Node) bool {
			return n.Data == "a" && attr(n, "id") == "top_avatar" && attr(n, "href") == "/"+alias
		})
		pageBody := find(doc, func(n *html.Node) bool { return n.Data == "body" && hasClass(n, "responses_index_page") })
		if identity == nil || pageBody == nil {
			return nil, fmt.Errorf("applications page does not confirm signed-in account %q; authentication or contract changed", alias)
		}
		tab := find(doc, func(n *html.Node) bool {
			return n.Data == "a" && attr(n, "href") == "/responses" && hasClass(n, "tab-item")
		})
		if tab == nil {
			return nil, fmt.Errorf("applications page has no native active-response count; stopped")
		}
		count := countPattern.FindStringSubmatch(nodeText(tab))
		if len(count) != 2 {
			return nil, fmt.Errorf("applications page has no readable active-response count; stopped")
		}
		nativeTotal, err := strconv.Atoi(count[1])
		if err != nil || total != -1 && nativeTotal != total {
			return nil, fmt.Errorf("native application count changed during pagination; stopped")
		}
		total = nativeTotal
		table := find(doc, func(n *html.Node) bool { return n.Data == "table" && hasClass(n, "my_responses") })
		if table == nil {
			empty := find(doc, func(n *html.Node) bool {
				return hasClass(n, "no-content__title") && nodeText(n) == "Нет откликов"
			})
			if total != 0 || empty == nil {
				return nil, fmt.Errorf("applications list is missing without a confirmed native empty state; stopped")
			}
		} else {
			for _, row := range findAll(table, func(n *html.Node) bool { return n.Data == "tr" }) {
				cells := findAll(row, func(n *html.Node) bool { return n.Data == "td" })
				if len(cells) == 0 {
					continue
				}
				if len(cells) != 3 {
					return nil, fmt.Errorf("application row has %d cells, expected 3; native contract changed", len(cells))
				}
				role := find(cells[0], func(n *html.Node) bool { return n.Data == "a" && strings.HasPrefix(attr(n, "href"), "/vacancies/") })
				company := find(cells[0], func(n *html.Node) bool { return n.Data == "a" && strings.HasPrefix(attr(n, "href"), "/companies/") })
				status := find(cells[2], func(n *html.Node) bool { return hasClass(n, "status") })
				if role == nil || company == nil || status == nil || nodeText(status) == "" {
					return nil, fmt.Errorf("application row lacks a confirmed role, employer or status; stopped")
				}
				id, err := domain.NewVacancyIDFromValue(Origin + attr(role, "href"))
				if err != nil || seen[id.String()] {
					return nil, fmt.Errorf("application row has a duplicate or invalid vacancy URL %q; stopped", attr(role, "href"))
				}
				date, err := time.Parse("02.01.2006", nodeText(cells[1]))
				if err != nil {
					return nil, fmt.Errorf("application %s has unsupported date %q; stopped", id.String(), nodeText(cells[1]))
				}
				seen[id.String()] = true
				rows = append(rows, Object{"vacancy_id": id.String(), "vacancy_url": Origin + "/vacancies/" + id.String(), "title": nodeText(role), "company": nodeText(company), "company_url": Origin + attr(company, "href"), "applied_on": date.Format("2006-01-02"), "status": nodeText(status)})
			}
		}
		nextPage, nextPath := 0, ""
		if paginator := find(doc, func(n *html.Node) bool { return attr(n, "id") == "paginator" }); paginator != nil {
			for _, link := range findAll(paginator, func(n *html.Node) bool { return n.Data == "a" }) {
				u, err := url.Parse(attr(link, "href"))
				if err != nil {
					return nil, fmt.Errorf("invalid native application pagination URL; stopped")
				}
				if u.IsAbs() && (u.Scheme != "https" || u.Host != "career.habr.com") || u.Host != "" && u.Host != "career.habr.com" || u.User != nil || u.Path != "/responses" || u.Fragment != "" || len(u.Query()) != 1 {
					return nil, fmt.Errorf("unsupported native application pagination URL; stopped")
				}
				n, err := strconv.Atoi(u.Query().Get("page"))
				if err != nil || n < 1 {
					return nil, fmt.Errorf("invalid native application page %q; stopped", u.Query().Get("page"))
				}
				if n > current && (nextPage == 0 || n < nextPage) {
					nextPage, nextPath = n, u.RequestURI()
				}
			}
		}
		if nextPath == "" {
			if len(rows) != total {
				return nil, fmt.Errorf("read %d applications but native count is %d; pagination contract may have changed", len(rows), total)
			}
			return Object{"account_alias": alias, "applications": rows}, nil
		}
		path, current = nextPath, nextPage
	}
	return nil, fmt.Errorf("application pagination exceeded 100 pages; stopped without a partial success result")
}
