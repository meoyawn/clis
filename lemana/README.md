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

The site was inspected through Chrome on 2026-10-27. Cookie-free requests, including ChatGPT-User, were rejected. `auth import` uses steipete/sweetcookie to read only anonymous protection and city cookies from the existing Chrome profile, storing them outside the checkout with mode 0600. Import/status make no HTTP requests. Use `--user-agent` during import if the browser version changes.

Protection responses, including challenge HTML with status 200, stop the command without retries. The entrypoint opens the page in Chrome (the storefront for POST endpoints); complete the check manually, import the same profile, and retry explicitly. Session import does not guarantee that the protection service accepts a separate HTTP client.

`list`/`search` make one GET and decode the JSON embedded in the server-rendered page. This already includes filter definitions, prices, delivery dates, characteristics, photos and ratings. It avoids the browser's separate `hybrid/v1/products:search`, `products-data:search` and `products-media:search` calls. Product-specific and grouped ratings remain separate. The generic SEO JSON-LD rating is deliberately ignored.

`details` makes one GET and includes the first review page. `--availability` adds one observed POST to `experience/LeroymerlinWebsite/v1/navigation-pdp-api/get-stocks`; the public API configuration is read from the same product HTML, never persisted or printed. No tracking, advertisements or shopping actions are replayed.

Validation: `moon run lemana:check`. Fixtures contain only anonymous public catalog fields or synthetic data, never sessions or browser captures.
