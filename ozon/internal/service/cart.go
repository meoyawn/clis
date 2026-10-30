package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

const addToCartPath = "/api/composer-api.bx/_action/v2/addToCart"
const maxCartRedirects = 3

type CartItem struct {
	SKU              string `json:"sku"`
	URL              string `json:"url"`
	Title            string `json:"title"`
	Variant          string `json:"variant"`
	Image            string `json:"image"`
	Quantity         *int64 `json:"quantity"`
	MaxOrderQuantity *int64 `json:"max_order_quantity,omitempty"`
	Selected         *bool  `json:"selected"`
	Prices           Prices `json:"prices"`
}

type CartResult struct {
	Metadata
	Count int        `json:"count"`
	Items []CartItem `json:"items"`
}

type CartMutationResult struct {
	Metadata
	SKU      string `json:"sku"`
	Quantity int64  `json:"quantity"`
}

func cartPageURL(raw string) (*url.URL, error) {
	clean, err := safePageURL(raw)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(clean)
	if err != nil {
		return nil, err
	}
	if u.Path != "/cart" && u.Path != "/cart/" {
		return nil, fmt.Errorf("cart redirect must stay on the Ozon cart page")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid cart redirect parameters")
	}
	for key, values := range q {
		if (key != "__rr" && key != "page_changed") || len(values) != 1 || values[0] == "" {
			return nil, fmt.Errorf("unsupported cart redirect parameters")
		}
	}
	u.RawQuery = q.Encode()
	return u, nil
}

func (c *Client) CartList(ctx context.Context, opt RequestOptions) (*CartResult, error) {
	path := "/cart"
	target := entrypointURL(path)
	cookies, err := c.auth.cookiesFor(target)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	jar.SetCookies(target, cookies)
	cookiesFor := func(u *url.URL) ([]*http.Cookie, error) { return jar.Cookies(u), nil }
	visited := map[string]bool{target.String(): true}
	var meta Metadata
	for redirects := 0; ; redirects++ {
		p, err := c.fetchPage(ctx, path, target, opt, cookiesFor)
		if p != nil {
			mergeMeta(&meta, p.meta)
			p.meta.HTTPRequests, p.meta.SourceURLs = meta.HTTPRequests, meta.SourceURLs
		}
		var redirect *pageRedirectError
		if !errors.As(err, &redirect) {
			if err != nil {
				return nil, err
			}
			return parseCart(p)
		}
		if redirects == maxCartRedirects {
			return nil, fmt.Errorf("%w: exceeded %d cart redirects", err, maxCartRedirects)
		}
		next, nextTarget, validationErr := pageRedirectURL(redirect.target, cartPageURL)
		if validationErr != nil {
			return nil, fmt.Errorf("%w: invalid cart redirect", err)
		}
		if visited[nextTarget.String()] {
			return nil, fmt.Errorf("%w: cart redirect loop", err)
		}
		visited[nextTarget.String()] = true
		jar.SetCookies(target, redirect.cookies)
		path, target = next.String(), nextTarget
	}
}

// Only 307/308 navigation redirects to the identical write endpoint are followed.
// They preserve the method and body; changing any parameter except __rr is rejected.
func cartWriteRedirectURL(raw string, current *url.URL) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" || strings.ContainsAny(raw, "\\\r\n") {
		return nil, fmt.Errorf("invalid cart write redirect")
	}
	next := current.ResolveReference(u)
	if next.Scheme != current.Scheme || next.Host != current.Host || next.EscapedPath() != current.EscapedPath() {
		return nil, fmt.Errorf("cart write redirect changed endpoint")
	}
	q, err := url.ParseQuery(next.RawQuery)
	if err != nil || q.Get("__rr") == "" {
		return nil, fmt.Errorf("cart write redirect lacks navigation state")
	}
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return nil, fmt.Errorf("invalid cart write redirect parameters")
		}
		if key != "__rr" && values[0] != current.Query().Get(key) {
			return nil, fmt.Errorf("cart write redirect changed parameters")
		}
	}
	for key, values := range current.Query() {
		if key != "__rr" && q.Get(key) != values[0] {
			return nil, fmt.Errorf("cart write redirect dropped parameters")
		}
	}
	return next, nil
}

func (c *Client) fetchCartWrite(ctx context.Context, relative string, target *url.URL, opt RequestOptions, payload []byte) (*jsonResponse, error) {
	cookies, err := c.auth.cookiesFor(target)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	jar.SetCookies(target, cookies)
	cookiesFor := func(u *url.URL) ([]*http.Cookie, error) { return jar.Cookies(u), nil }
	visited := map[string]bool{target.String(): true}
	var meta Metadata
	for redirects := 0; ; redirects++ {
		response, err := c.fetchJSON(ctx, relative, target, opt, cookiesFor, payload)
		if response != nil {
			mergeMeta(&meta, response.meta)
			response.meta.HTTPRequests, response.meta.SourceURLs = meta.HTTPRequests, meta.SourceURLs
		}
		var redirect *pageRedirectError
		var failure *HTTPError
		if !errors.As(err, &redirect) || !errors.As(err, &failure) || (failure.Status != http.StatusTemporaryRedirect && failure.Status != http.StatusPermanentRedirect) {
			return response, err
		}
		if redirects == maxCartRedirects {
			return response, fmt.Errorf("%w: exceeded %d cart write redirects", err, maxCartRedirects)
		}
		next, validationErr := cartWriteRedirectURL(redirect.target, target)
		if validationErr != nil {
			return response, fmt.Errorf("%w: invalid cart write redirect", err)
		}
		if visited[next.String()] {
			return response, fmt.Errorf("%w: cart write redirect loop", err)
		}
		visited[next.String()] = true
		jar.SetCookies(target, redirect.cookies)
		target = next
	}
}

// CartAdd sends the captured single-unit add action without reading or changing other items.
func (c *Client) CartAdd(ctx context.Context, ref ItemReference, opt RequestOptions) (*CartMutationResult, error) {
	if !skuPattern.MatchString(ref.SKU) {
		return nil, fmt.Errorf("invalid SKU %q", ref.SKU)
	}
	payload, err := json.Marshal([]struct {
		ID       json.Number `json:"id"`
		Quantity int         `json:"quantity"`
	}{{ID: json.Number(ref.SKU), Quantity: 1}})
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(WebOrigin + addToCartPath)
	if err != nil {
		return nil, err
	}
	result, err := c.fetchCartWrite(ctx, "/product/"+ref.SKU+"/", target, opt, payload)
	if err != nil {
		return nil, fmt.Errorf("add SKU %s to cart: %w", ref.SKU, err)
	}
	var response struct {
		Data struct {
			Success bool `json:"success"`
			Cart    struct {
				Items []struct {
					ID       json.Number `json:"id"`
					Quantity int64       `json:"qty"`
				} `json:"cartItems"`
			} `json:"cart"`
		} `json:"data"`
	}
	if json.Unmarshal(result.body, &response) == nil && response.Data.Success {
		for _, item := range response.Data.Cart.Items {
			if item.ID.String() == ref.SKU && item.Quantity > 0 {
				return &CartMutationResult{Metadata: result.meta, SKU: ref.SKU, Quantity: item.Quantity}, nil
			}
		}
	}
	return nil, fmt.Errorf("Ozon did not confirm adding SKU %s; check ozon cart list before retrying", ref.SKU)
}

// CartRemove deletes only this SKU, including all its units. Failed writes are never retried.
func (c *Client) CartRemove(ctx context.Context, ref ItemReference, opt RequestOptions) (*CartMutationResult, error) {
	if !skuPattern.MatchString(ref.SKU) {
		return nil, fmt.Errorf("invalid SKU %q", ref.SKU)
	}
	params, err := json.Marshal(struct {
		Items []string `json:"items"`
	}{Items: []string{ref.SKU}})
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(struct {
		Name   string `json:"name"`
		Params string `json:"params"`
	}{Name: "deleteItems", Params: string(params)})
	if err != nil {
		return nil, err
	}
	path := "/cart?page_changed=true"
	response, err := c.fetchCartWrite(ctx, "/cart", entrypointURL(path), opt, payload)
	if err != nil {
		return nil, fmt.Errorf("remove SKU %s from cart: %w", ref.SKU, err)
	}
	p, err := decodePageResponse(response)
	if err != nil {
		return nil, err
	}
	cart, err := parseCart(p)
	if err != nil {
		return nil, err
	}
	for _, item := range cart.Items {
		if item.SKU == ref.SKU {
			return nil, fmt.Errorf("Ozon still reports SKU %s in the cart; removal was not confirmed", ref.SKU)
		}
	}
	return &CartMutationResult{Metadata: cart.Metadata, SKU: ref.SKU, Quantity: 0}, nil
}

func parseCart(p *page) (*CartResult, error) {
	loggedIn, err := p.loginState()
	if err != nil {
		return nil, err
	}
	if loggedIn != nil && !*loggedIn {
		return nil, fmt.Errorf("Ozon returned a signed-out cart; re-import your browser session")
	}
	widgets, err := p.widgets("cartSplit")
	if err != nil {
		return nil, err
	}
	out := &CartResult{Metadata: p.meta, Items: []CartItem{}}
	if len(widgets) == 0 {
		empty, err := p.first("emptyCart")
		if err != nil {
			return nil, err
		}
		if empty == nil {
			return nil, fmt.Errorf("Ozon page has no cart items or empty-cart state; cart schema may have changed")
		}
		return out, nil
	}
	for _, widget := range widgets {
		items, ok := widget["cartItems"].([]any)
		if !ok {
			return nil, fmt.Errorf("Ozon cart widget has no cartItems array")
		}
		for _, raw := range items {
			product := at(raw, "product")
			sku := str(at(product, "id"))
			if !skuPattern.MatchString(sku) {
				return nil, fmt.Errorf("Ozon cart item has an invalid or missing SKU")
			}
			item := CartItem{SKU: sku, URL: WebOrigin + "/product/" + sku + "/",
				Image:            publicURL(str(at(product, "image", "image", "url"))),
				Quantity:         integer(at(raw, "controls", "quantity", "current")),
				MaxOrderQuantity: integer(at(raw, "controls", "quantity", "maximum")),
				Selected:         boolean(at(raw, "checkbox", "isChecked"))}
			for _, column := range arr(at(product, "titleColumn")) {
				if str(at(column, "type")) != "text" {
					continue
				}
				value := text(at(column, "text"))
				if item.Title == "" {
					item.Title = value
				} else if value != "" {
					item.Variant = strings.TrimSpace(item.Variant + " " + value)
				}
			}
			for _, column := range arr(at(product, "priceColumn")) {
				for _, element := range arr(at(column, "prices", "elements")) {
					price := money(text(at(element, "price")))
					switch str(at(element, "priceStyle", "styleType")) {
					case "CARD_PRICE":
						item.Prices.WithOzonCard = price
					case "SECOND_LVL", "PRICE":
						item.Prices.WithoutOzonCard = price
					}
				}
			}
			out.Items = append(out.Items, item)
		}
	}
	out.Count = len(out.Items)
	return out, nil
}
