# santehnika

Read-only public catalog for https://santehnika-online.ru/, with Kazan as the region. Run from this directory:

```sh
go run . --help
go run . auth import --browser chrome --browser-profile Default
go run . list /smesiteli/na_kukhnyu/ --include-filters
go run . search 'смеситель для кухни' --limit 10
go run . details PRODUCT_URL
```

Flags may precede or follow positional arguments. `--filter KEY=VALUE` repeats values from the returned filter definitions; `--limit` trims one server page without requesting more products. Images are returned as URLs, never fetched. Download selected URLs directly with curl and view locally.

The site was inspected through Chrome on 2026-10-27. Cookie-free requests, including ChatGPT-User, were rejected. `auth import` uses steipete/sweetcookie to copy the storefront session from the existing Chrome profile, including the site session, city, fingerprint and CAPTCHA-clearance cookies. Cookies are restricted to the storefront domain and root path, and stored outside the checkout with mode 0600; values are never printed. Cookies from other sites are excluded. Import/status make no HTTP requests. Use `--user-agent` during import if the browser version changes.

Protection responses, including challenge HTML with status 200, stop the command without retries. The entrypoint opens the page in Chrome (the storefront for POST endpoints); complete the check manually, import the same profile, and retry explicitly. The HTTP client uses a fixed Chrome-compatible TLS/HTTP2 profile, browser request headers and the last catalog page as its Referer. It does not rotate browser identities or solve challenges.

`search` makes one form POST to `/ajax/react/searchProducts/`. `list --section ID` uses `/ajax/react/productList/`; section 5316 is kitchen mixers. Both return compact JSON. Section requests preserve the observed `pageType=category` and `perpage=69` fields. The first HTML page omits default pagination/sort parameters to avoid a redundant redirect. `search --include-filters` and `list CATEGORY_URL --include-filters` instead make one GET, decoding the embedded `stateSd` catalog and filter definitions.

`details` makes one GET and reads the product JSON-LD plus server-rendered characteristic rows. Mounting dimensions may only be published in the dimension drawing. Grouped review totals are not presented as product-specific totals. No delivery estimator, advertising, recommendations or analytics are requested. Live checks on 2026-10-27 verified a Kazan category GET, filtered JSON POST and product GET at one request each. Kazan is validated against saved city cookies and the server-rendered page location; another city causes an error instead of a misleading Kazan result.

Validation: `moon run santehnika:check`. Fixtures contain only anonymous public catalog fields or synthetic data, never sessions or browser captures.
