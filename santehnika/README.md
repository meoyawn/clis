# santehnika

Read-only public catalog for https://santehnika-online.ru/, with Kazan as the region. Run from this directory:

```sh
go run . --help
go run . auth import --browser chrome --browser-profile Default
go run . list /smesiteli/na_kukhnyu/ --include-filters
go run . search 'смеситель для кухни' --limit 10
go run . details PRODUCT_URL
go run . availability API_ID_FROM_SEARCH_OR_LIST
```

Flags may precede or follow positional arguments. `--filter KEY=VALUE` repeats values from the returned filter definitions; `--limit` trims one server page without requesting more products. Images are returned as URLs, never fetched. Download selected URLs directly with curl and view locally.

The site was inspected through Chrome on 2026-10-27. Cookie-free requests, including ChatGPT-User, were rejected. `auth import` uses steipete/sweetcookie to copy the storefront session from the existing Chrome profile, including the site session, city, fingerprint and CAPTCHA-clearance cookies. Cookies are restricted to the storefront domain and root path, and stored outside the checkout with mode 0600; values are never printed. Cookies from other sites are excluded. Import/status make no HTTP requests. Use `--user-agent` during import if the browser version changes.

Protection responses, including challenge HTML with status 200, stop the command without retries. The entrypoint opens the page in Chrome (the storefront for POST endpoints); complete the check manually, import the same profile, and retry explicitly. The HTTP client uses a fixed Chrome-compatible TLS/HTTP2 profile, browser request headers and the last catalog page as its Referer. It does not rotate browser identities or solve challenges.

`search` makes one form POST to `/ajax/react/searchProducts/`. `list --section ID` uses `/ajax/react/productList/`; section 5316 is kitchen mixers. Both return compact JSON. Section requests preserve the observed `pageType=category` and `perpage=69` fields. The first HTML page omits default pagination/sort parameters to avoid a redundant redirect. `search --include-filters` and `list CATEGORY_URL --include-filters` instead make one GET, decoding the embedded `stateSd` catalog and filter definitions.

`rating` and `review_count` always refer to the exact product. `product_rating` and `product_review_count` retain those same values for existing consumers. Variant totals live only in `group_rating` and `group_review_count`. Unknown own counters remain `null`; an explicit zero remains zero, even if the group has many reviews. Search/list include both scopes in their one response.

`details` makes one GET and reads the product JSON-LD plus server-rendered characteristic rows. That HTML exposes an aggregate without a verified exact-product counter, so only `group_*` is populated; the exact-product review fields remain `null`. Use search/list for exact-product review counts. Mounting dimensions may only be published in the dimension drawing. No advertising, recommendations or analytics are requested. Live checks on 2026-10-27 verified a Kazan category GET, filtered JSON POST and product GET at one request each. Kazan is validated against saved city cookies and the server-rendered page location; another city causes an error instead of a misleading Kazan result.

`availability API_ID` uses `api_id` from search/list, not the product code in its URL. It makes the single observed GET to `/ajax/react/deliveryCalculation/` with Kazan and the product's backend ID, returning the site's JSON calculation in `availability`. It does not load product HTML, reserve stock or place an order. Use the same saved browser session and validate the returned dates before promising pickup.

Validation: `moon run santehnika:check`. Fixtures contain only anonymous public catalog fields or synthetic data, never sessions or browser captures.
