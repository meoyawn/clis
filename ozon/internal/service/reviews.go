package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (c *Client) Reviews(ctx context.Context, ref ItemReference, o ReviewsOptions, opt RequestOptions) (*ReviewsResult, error) {
	path, err := ReviewsPath(ref, o)
	if err != nil {
		return nil, err
	}
	p, err := c.fetchProduct(ctx, path, ref.SKU, opt)
	if err != nil {
		return nil, err
	}
	return parseReviews(p, ref, path)
}
func parseReviews(p *page, ref ItemReference, path string) (*ReviewsResult, error) {
	w, err := p.first("webListReviews")
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("Ozon response has no reviews list")
	}
	if str(w["itemId"]) != ref.SKU {
		return nil, fmt.Errorf("Ozon reviews belong to SKU %q, requested %s", str(w["itemId"]), ref.SKU)
	}
	requested := sourcePageNumber(path)
	actual := int(intValue(at(w, "paging", "page")))
	if actual != requested {
		return nil, fmt.Errorf("Ozon returned review page %d, requested %d", actual, requested)
	}
	out := &ReviewsResult{
		Metadata: p.meta, SKU: ref.SKU, Rating: number(w["productScore"]),
		Pagination: Pagination{Page: actual, Total: integer(at(w, "paging", "total"))},
		Reviews:    []Review{},
	}
	requestURL, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	out.Scope = "all"
	if requestURL.Query().Get("reviewsVariantMode") == "1" {
		out.Scope = "sku"
	}
	out.Sort = requestURL.Query().Get("sort")
	out.AllVariantsTotal = integer(at(w, "paging", "commonTotal"))
	raw, ok := w["reviews"].([]any)
	if !ok {
		return nil, fmt.Errorf("Ozon reviews widget has no reviews array")
	}
	for _, v := range raw {
		r := Review{
			ID: str(at(v, "uuid")), SKU: str(at(v, "itemId")),
			Author: text(at(v, "author", "firstName")), Rating: number(at(v, "content", "score")),
			Text: str(at(v, "content", "comment")), Pros: str(at(v, "content", "positive")), Cons: str(at(v, "content", "negative")),
			Media: []Media{}, Variant: []Attribute{}, VerifiedPurchase: boolean(at(v, "isItemPurchased")),
			Helpful: intValue(at(v, "usefulness", "useful")), Unhelpful: intValue(at(v, "usefulness", "unuseful")),
			CommentsCount: intValue(at(v, "comments", "totalCount")),
		}
		if r.ID == "" || !skuPattern.MatchString(r.SKU) {
			return nil, fmt.Errorf("Ozon returned a review without ID or SKU")
		}
		if out.Scope == "sku" && r.SKU != ref.SKU {
			return nil, fmt.Errorf("Ozon ignored the SKU review filter: received SKU %s, requested %s", r.SKU, ref.SKU)
		}
		// Keep the review UUID in its permalink, discard share tracking parameters.
		r.URL = WebOrigin + ref.Path + "reviews/?ruuid=" + url.QueryEscape(r.ID)
		r.PublishedAt = timestamp(at(v, "publishedAt"))
		if r.PublishedAt == nil {
			r.PublishedAt = timestamp(at(v, "createdAt"))
		}
		r.UpdatedAt = timestamp(at(v, "updatedAt"))
		for _, variant := range arr(at(w, "products", r.SKU, "variants")) {
			r.Variant = append(r.Variant, Attribute{Name: str(at(variant, "name")), Values: []string{str(at(variant, "value"))}})
		}
		for _, kind := range []string{"photos", "videos"} {
			for _, m := range arr(at(v, "content", kind)) {
				media := Media{Type: "photo", ID: str(at(m, "UUID")), URL: publicURL(str(at(m, "url"))), Width: int(intValue(at(m, "width"))), Height: int(intValue(at(m, "height")))}
				if kind == "videos" {
					media.Type = "video"
					media.PreviewURL = publicURL(str(at(m, "previewUrl")))
					media.DurationSeconds = durationSeconds(str(at(m, "duration")))
				}
				if media.URL == "" {
					return nil, fmt.Errorf("Ozon review %s contains media without a usable URL", r.ID)
				}
				r.Media = append(r.Media, media)
			}
		}
		out.Reviews = append(out.Reviews, r)
	}
	if next := str(at(w, "paging", "nextButton")); next != "" {
		// The continuation's query is merged with the current query so scope and
		// sorting survive even when nextButton only contains page/page_key.
		base, err := url.Parse(path)
		if err != nil {
			return nil, err
		}
		relative, e := url.Parse(next)
		if e != nil {
			return nil, fmt.Errorf("invalid review continuation URL")
		}
		if relative.Path == "" && !relative.IsAbs() && relative.Host == "" {
			q := base.Query()
			for k, vs := range relative.Query() {
				q[k] = vs
			}
			base.RawQuery = q.Encode()
		} else {
			safe, e := safePageURL(next)
			if e != nil {
				return nil, e
			}
			base, err = url.Parse(safe)
			if err != nil {
				return nil, err
			}
		}
		clean, e := ReviewsPath(ref, ReviewsOptions{Page: 1, Cursor: base.String(), Sort: UsefulReviewSort(), Scope: AllReviewScope()})
		if e != nil {
			return nil, e
		}
		n := sourcePageNumber(clean)
		if n <= actual {
			return nil, fmt.Errorf("Ozon returned a non-advancing review cursor")
		}
		out.HasMore = true
		out.NextPage = &n
		out.NextCursor = clean
	}
	out.Count = len(out.Reviews)
	return out, nil
}
func timestamp(v any) *time.Time {
	n := intValue(v)
	if n <= 0 {
		return nil
	}
	t := time.Unix(n, 0).UTC()
	return &t
}
func durationSeconds(s string) *int {
	if s == "" {
		return nil
	}
	total := 0
	for _, p := range strings.Split(s, ":") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		total = total*60 + n
	}
	return &total
}
