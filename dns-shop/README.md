# dns-shop

Read-only public catalog for https://www.dns-shop.ru/: search with the site's filters, crossed-out prices, ratings and grouped characteristics. Results are JSON on stdout. Run from this directory:

```sh
go run . --help
go run . auth import --browser chrome --browser-profile Default
go run . search 'кабель micro usb' --include-filters
go run . search 'кабель micro usb' --category 17a8a26516404e77 --filter brand=aceline --sort price-asc
go run . details https://www.dns-shop.ru/product/ec2cd41a774d1983/kabel-aceline-micro-usb-b-usb-type-a-belyj/
go run . details 9086540
```

Flags may precede or follow positional arguments.

## Session and city

The site is behind Qrator; cookie-free requests get HTTP 401. `auth import` uses steipete/sweetcookie to read only the anonymous `qrator_*` protection cookies, the `current_path`/`city_path` city cookies and `lang` from the existing Chrome profile. They are stored outside the checkout with mode 0600. Account (`auth_*`), cart, phone and analytics cookies are never imported. Import/status make no HTTP requests.

When the saved `qrator_jsid2` cookie is stale, or a request hits a protection response, the command re-reads Chrome's cookies, opens the page in a background Chrome tab so Chrome renews them, and retries once. If Chrome holds no fresh cookie within 90 seconds, the entrypoint brings the page to the front; complete the check manually and rerun.

Prices and stock depend on the city in the server-signed `current_path` cookie. The unsigned `city_path` cookie alone does not change it: on 2026-11-26, requests with `city_path=moscow` or `spb` still returned the Kazan storefront. So the CLI does not switch cities. Instead, `--city` (default `kazan`) asserts the imported session's city and refuses to run on a mismatch. To use another city, select it on dns-shop.ru in Chrome, rerun `auth import`, and pass `--city SLUG`, as in `/shops/SLUG/`.

## Requests

| Command | HTTP requests |
| --- | --- |
| `search` | page GET + prices POST (`/ajax-state/product-buy/`) |
| `search` without `--category` but with sort/page/filters | +1 GET: the category redirect drops them |
| `search --include-filters` | +1 GET `/catalog/search/filters/` |
| `details URL` | GET `/product/characteristics/…` + GET `/product/microdata/GUID/` |
| `details CODE` | +1 search GET that redirects to the product |
| `auth import`, `auth status`, `--help` | 0 |

The site was inspected through Chrome on 2026-11-26. Cards come from server-rendered HTML; the site loads prices separately, so the CLI replays only that state request with the page's CSRF token and hash. Availability widgets, wishlist, analytics, recommendations and tracking are not requested. Images are returned as URLs and never fetched.

Validation: `moon run dns-shop:check`. Fixtures in `internal/service/testdata` are trimmed public catalog pages with a synthetic CSRF token, never sessions or browser captures.
