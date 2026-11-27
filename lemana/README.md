# lemana

Read-only public catalog for https://kazan.lemanapro.ru/, with Kazan as the region. Run from this directory:

```sh
go run . --help
go run . auth import --browser chrome --browser-profile Default
go run . list /catalogue/kuhonnye-smesiteli/ --include-filters
go run . search 'смеситель для кухни' --limit 10
go run . details PRODUCT_URL
```

Flags may precede or follow positional arguments. `--filter KEY=VALUE` repeats values from the returned filter definitions; `--limit` trims one server page without requesting more products. Images are returned as URLs, never fetched. Download selected URLs directly with curl and view locally.

The site was inspected through Chrome on 2026-10-27. Cookie-free requests, including ChatGPT-User, were rejected: every request needs the qrator protection cookie `qrator_jsid2`, which lives about 16 minutes. When the saved cookie is missing or stale, or a request hits a protection response (including challenge HTML with status 200 and HTTP 401/403/429), the command loads the storefront in a hidden system web view ([mygo](https://github.com/egoist/mygo): WKWebView on macOS, WebView2 on Windows, WebKitGTK on Linux) with a fresh in-memory session. The protection service's own script runs there unmodified; the CLI never computes the check. The challenge reloads the page by script, which reports no navigation start, so once `qrator_jsid2` exists the web view loads the storefront once more and accepts the cookie when that page finishes. The CLI then copies only `qrator_*` cookies into the session file, keeps the region cookies, and retries the command once, within 30 seconds for the web view. This takes a few seconds, works under `go run`, never opens a browser and writes nothing from the web view to disk.

`auth import` is optional: it seeds the session from the existing Chrome profile with steipete/sweetcookie, reading only anonymous protection and city cookies and storing them outside the checkout with mode 0600. Import/status make no HTTP requests.

`list`/`search` make one GET and decode the JSON embedded in the server-rendered page. This already includes filter definitions, prices, delivery dates, characteristics, photos and ratings. It avoids the browser's separate `hybrid/v1/products:search`, `products-data:search` and `products-media:search` calls. The generic SEO JSON-LD rating is deliberately ignored.

`rating` and `review_count` always refer to the exact product. `product_rating` and `product_review_count` retain those same values for existing consumers. Variant totals live only in `group_rating` and `group_review_count`. Unknown own counters remain `null`; an explicit zero remains zero, even if the group has many reviews. The website's sort order may use grouped scores, so apply exact-product rating/count thresholds to the returned products locally.

`details` makes one GET and includes the first review page and the variant group from `relatedProducts.variants`: every existing product of the group with its price and axis values (for example wattage and light color). The selector greys out ("punctures") values with no product for the current combination, so a combination missing from `variants` is not sold, rather than out of stock. `--availability` adds one observed POST to `experience/LeroymerlinWebsite/v1/navigation-pdp-api/get-stocks`; the public API configuration is read from the same product HTML, never persisted or printed. No tracking, advertisements or shopping actions are replayed.

Validation: `moon run lemana:check`. Fixtures contain only anonymous public catalog fields or synthetic data, never sessions or browser captures.
