# ozon

A Go CLI for finding Ozon products, checking exact variants and prices, comparing
sellers, reading reviews, and managing the shopping cart. Data commands return JSON. Prices
and delivery reflect the saved account's Ozon location and session.

Agent workflow: [ozon skill](.agents/skills/ozon/SKILL.md).

## Installation

Requires Go 1.27.2 or newer. Install from this checkout:

```sh
go install -trimpath .
ozon --help
```

For a binary inside the checkout, use
`go build -trimpath -o bin/ozon .`.

Sign in to Ozon in a supported browser, then import its session once:

```sh
ozon auth import --browser chrome
# Select a profile if needed:
ozon auth import --browser chrome --browser-profile 'Profile 1'
```

`ozon auth status` verifies the CLI's saved cookies without reading the browser
or updating the saved session. Product and search commands use that same copy.
`auth import` copies browser cookies locally with sweetcookie and makes zero
HTTP requests. The CLI never opens a browser or uses Chrome's renewal tokens.

## Commands

| Need | Command | HTTP requests per invocation |
| --- | --- | ---: |
| Find candidates and discover filters together | `ozon search 'query' --include-filters` | 1, plus up to 3 validated redirects |
| Search using known constraints | `ozon search 'query' --filter KEY=VALUE` | 1, plus up to 3 validated redirects |
| Discover filters without product output | `ozon filters 'query'` | 1, plus up to 3 validated redirects |
| Inspect prices, variants and the current seller | `ozon show <sku-or-url>` | 1, plus up to 3 validated redirects |
| Load details and compare seller offers | `ozon show <sku-or-url> --full` | At most 4 pages, each plus up to 3 validated redirects |
| Read one page of buyer reviews | `ozon reviews <sku-or-url>` | 1, plus up to 3 validated redirects |
| List cart items | `ozon cart list` | 1, plus up to 3 validated cart redirects |
| Add one unit to the cart | `ozon cart add <sku-or-url>` | 1 POST, plus up to 3 validated 307/308 navigation redirects |
| Remove all units of one SKU | `ozon cart remove <sku-or-url>` | 1 POST, plus up to 3 validated 307/308 navigation redirects |
| Copy browser session | `ozon auth import --browser chrome` | 0 |
| Check saved authentication | `ozon auth status` | 1, plus up to 3 validated redirects |

**Behavior change:** `show` is now lightweight by default. Existing scripts that
need the additional description/characteristics section or other seller offers
must use `show --full`.

No command automatically visits each result, variant, review attachment, or
seller. `--include-filters` parses the same search response and adds no requests.
`show --full` skips optional sections when Ozon supplies no link to them.

## JSON contract

### Shared fields

Successful responses include `http_requests`, `fetched_at`, and `source_urls`.
`warnings` describes limitations such as a signed-out page or an inferred brand.
`is_authenticated` is present when Ozon supplies it. Source URLs and cursors can
contain session-specific state; keep them out of public reports.

Money is `{ "amount": 4517, "currency": "RUB", "text": "4 517 ₽" }`.
Use `amount` for comparisons, and retain the currency and conditions when
reporting it. Unknown numeric/boolean values are `null`, not zero or false.

### Search and filters

`search` returns `query`, `results`, and pagination fields. Each product has:

- `sku`, canonical `url`, `title`, and `images` URLs;
- displayed `price`, `rating`, and `num_ratings`;
- `quantity` only from an explicit stock label, with `quantity_source`;
- `max_order_quantity` when Ozon supplies a cart limit; this is **not stock**;
- `delivery_day` in Ozon's wording, or an empty string if absent.

With `--include-filters`, the same top-level `filters` and `sorting` arrays as
`ozon filters` are added. Without the flag, these fields are omitted. If an
incremental page has no facets, both are `null` with a warning; no extra page is
fetched. An explicit no-results response returns empty arrays.

Each filter exposes `key`, `type`, `title`, `description`, `selected`, `multiple`,
`min`, `max`, `options`, and `options_truncated`. Options expose `value`, `label`,
`selected`, and any supplied URL/image. No separate request expands truncated
option lists.

### Lightweight and full product details

Both `show` modes return the product fields plus `prices`, `available`, gallery
images, `variants`, and the current seller when supplied.

| Field | `show` (`full: false`) | `show --full` (`full: true`) |
| --- | --- | --- |
| `prices` | Current SKU's price tiers | Same, from the product page |
| `variants` | Options supplied for the current selection | Same; variant pages are not fetched |
| `sellers` | Current seller only | Current seller plus the first cheaper/faster offer lists |
| `description`, `description_images`, `characteristics` | Whatever is already on the main page | Also merges the additional details section when present |
| `sellers_truncated` | Does not establish completeness; comparisons were not requested | Indicates whether Ozon reported more seller offers beyond those loaded |

`full` records the requested fetch mode. It does not guarantee that every field
exists or that all seller offers were fetched. Empty detail fields in lightweight
mode mean no such data was present on the main page; try `--full` if needed.

`prices.with_ozon_card` and `prices.without_ozon_card` preserve Ozon's two price
tiers; `card_label` and `regular_label` retain their displayed conditions. The
discounted tier can also apply to partner banks. `prices.original`, when present,
is a separate list price, not the ordinary payment price. Top-level `price` is
the displayed/default tier; it is not an unconditional quote.

Product `quantity` comes from `freeRest`. Seller entries include their own SKU,
product URL, prices, delivery label, `current`, `seen_in`, and any supplied price
or delivery ranking. Missing seller ratings/prices remain `null`. Current-seller
delivery can be absent even when the search card showed a date.

Full Ozon product URLs are accepted, including product review/features URLs;
tracking parameters are removed. Short `/t/` links and unrelated URLs are rejected.

### Reviews

`reviews` returns `scope`, `sort`, pagination, `all_variants_total`, Ozon's
aggregate `rating`, and a `reviews` array. A review contains its ID, SKU,
permalink, public author name, rating, timestamps, text, pros/cons, variant
attributes, purchase flag, helpful/unhelpful counts, and comment count. Comments
are not individually fetched.

`media` contains original photo and video URLs. Photo entries can include
`width` and `height`; video entries can additionally include `preview_url` and
`duration_seconds`. Photos precede videos, preserving server order within each
type. Missing media metadata is omitted. Media is not downloaded.

### Cart

```sh
ozon cart add https://www.ozon.ru/product/1613489220/
ozon cart list
ozon cart remove 1613489220
```

`cart list` returns `count` and `items` from all cart-item sections on the cart
page, excluding recommendation widgets and their pagination. Each item includes
its SKU, canonical URL, title, variant text, image, quantity, selection state,
and both price tiers when supplied. Missing quantities/prices remain `null`.
An explicit empty-cart response returns `items: []`; an unrecognized page fails.

`cart add` sends quantity 1 for the exact SKU. `cart remove` deletes all units
of only that SKU, leaving other products alone. Both return `sku`, `success`,
and the acknowledged `quantity` (0 after removal). Addition requires Ozon's
success acknowledgement for that SKU; deletion requires the returned cart to
exclude it. These commands change the saved session's cart and do not check out.

Cart writes follow only HTTP 307/308 redirects to the identical endpoint with
unchanged parameters except Ozon's `__rr` navigation state, preserving the POST
body. No failed mutation is automatically retried. If a request fails after it
was sent, use `cart list` to check the outcome before repeating it.

## Pagination

`search` and `reviews` fetch **one server page per invocation**:

- `count`: number of items printed in this response;
- `total`: Ozon's supplied total, or `null` when unavailable;
- `has_more`, `next_page`, `next_cursor`: whether/how to continue;
- `message`: contextual information, such as an explicit no-results response.

Use the returned `next_cursor` with `--cursor` to continue the same search or
product reviews. `--page N` is also available. A cursor includes the query,
filters, sorting, and review scope; it cannot be combined with `--page`,
`--filter`, `--sort`, or `--scope`. `--limit` and search `--include-filters` are
allowed with cursors. Later search pages may not contain facets.

**`--limit` trims output; it does not reduce the HTTP request or fetch more pages.**
The default is `0` (print the whole fetched page). A limited response's cursor
advances past the entire server page, including results omitted from output.
An explicit “nothing found” response has `total: 0` and `has_more: false`.
Ozon's recommendation continuations are omitted.

## Navigation and errors

Search exports only the layout's `catalog.searchResultsV2` grid. Other carousels,
unknown/ambiguous result sources, and explicit recommendation feeds are rejected.
Query, sorting and explicit filter changes are errors. Full-page active facets
must confirm requested filters; echoed URL parameters alone are insufficient.
With `currency_price`, cards with out-of-range or unknown displayed prices are
omitted and reported through `excluded_count` and `warnings`.

Ozon may redirect a query into a category or **infer a brand**. A new brand filter
is accepted when Ozon marks `brand_was_predicted=true` and no brand was already
set. The inferred brand is reported in `warnings` and retained in pagination.
A caller-selected brand cannot be replaced or dropped, and an adopted brand must
remain stable. Prediction does not establish an exact textual model match.

Each search/filter, authentication, product, details, seller-offer, or review page
follows at most three validated redirects, including request-local API navigation
state and updated cookies. Those hops count toward `http_requests`. Product
redirects must retain the SKU, page section, and requested parameters, including
review scope, pagination, and seller sorting. There are no automatic retries,
background requests, response caches,
or browser-automation fallbacks.

All commands accept `--timeout 60s` (whole invocation) and `--user-agent TEXT`.
Flags may appear before or after positional arguments. Use `--` before a query
beginning with a dash. Errors go to stderr and exit nonzero; failed commands do
not produce a successful empty JSON result. Help, invalid input, and missing or
fully expired saved credentials make zero HTTP requests.

## Authentication and local data

Supported browsers: `chrome`, `firefox`, `brave`, `edge`, `safari`. Browser cookie
import uses [sweetcookie](https://github.com/steipete/sweetcookie), following
[spogo's import implementation](https://github.com/openclaw/spogo/blob/main/internal/cli/auth_import.go).
OS Keychain/keyring access may be required.

`auth import` copies the selected browser's session cookies without contacting
Ozon. It preserves the session credentials as read by sweetcookie and excludes
refresh and login-exchange tokens. Failed reads preserve the previous copy.
`auth status` checks the saved copy through Ozon's `/my/main` page API without
reading the browser or saving cookies. Redirect challenge cookies stay in
memory for that check; redirects cannot replace the imported account tokens.
All CLI requests exclude renewal credentials, including those saved by older
versions. No command opens a browser or writes browser cookies.

Imports record the actual cookie-store path; mixed-profile sets are rejected.
Select one profile with `--browser-profile` when necessary. To refresh an expired
session, run `auth import` again. A browser challenge requires visiting ozon.ru
yourself in the selected browser profile before importing again.

Import prints the number of cookies saved; status reports the verified account.
`--json` adds cookie names/counts, expiry diagnostics and request metadata, never
cookie values. Import reports `login_verified: false` and `http_requests: 0`;
only status verifies the account online.
Credentials are saved atomically with mode `0600` in a `0700` directory:

- macOS: `~/Library/Application Support/ozon/auth.json`
- Linux: `$XDG_CONFIG_HOME/ozon/auth.json`, defaulting to `~/.config/ozon/auth.json`

Keep captures, credentials and command output outside the repository. HARs can
contain cookies/account data; auth JSON identifies the account and browser store;
source URLs and cursors can contain session state. Use synthetic fixtures for
contributions. The CLI makes direct requests with Go's HTTP/TLS transport.

## Development and validation

From this monorepo, run:

```sh
moon run ozon:check
# Or separately:
moon run ozon:test
moon run ozon:lint
moon run ozon:build
```

Tests run with the race detector. Moon's Go plugin installs the pinned compiler
and golangci-lint; every CLI uses the root `.golangci.yml`.
Normal tests use synthetic responses and require no Ozon session. They cover
request counts, lightweight/full product modes, combined facets, prediction and
pagination, explicit filter preservation, prices, stock, variants, reviews,
authentication and secret-free output.

For optional private HAR replay, set `OZON_HAR_DIR` to an external capture folder
and run `go test ./internal/service -run TestPrivateHARs -v`.

After request or authentication changes, install the updated binary and verify
live authentication, search with facets, standalone filters, and both product
fetch modes against a signed-in browser. Check request counts and exact variant
data; fixture tests alone do not establish live compatibility.

The spogo MIT notice is retained in [docs/spogo-LICENSE](docs/spogo-LICENSE).
