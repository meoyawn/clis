# invitro

An HTTP-only Go CLI for the public [Invitro catalog](https://www.invitro.ru/), with Planeta-style commands and JSON output. Requires Go 1.27.2+.

```sh
go install .
invitro config set city kazan
invitro search ттг
invitro show 2467
invitro show 'https://www.invitro.ru/analizes/for-doctors/kazan/629/2467/' --full
```

To build without installing:

```sh
go build -o invitro .
```

## Commands

| Planeta | Invitro |
| --- | --- |
| `planeta search <query>` | `invitro search <query>` |
| `planeta id <id>` | `invitro show <id-or-url>` |
| `planeta config` | `invitro config` |
| `planeta config set city <slug>` | `invitro config set city <slug>` |

There is no `invitro id` command. Options can appear before or after positional arguments. A literal `--` ends option parsing. Success writes one JSON object to stdout; errors go to stderr with a nonzero exit status. Help is plain text.

## Anonymous access

Live checks on 2026-09-19 confirmed that city lookup, search, filters, ID mapping, and details work without cookies or an account. The client sends the public website's `Web: true` header and a browser-style User-Agent. A plain curl User-Agent was rejected during investigation.

Sweet Cookie and browser-cookie import are unnecessary. The CLI has no authentication commands or cookie-file options.

If access checks change, the CLI reports the rejection immediately. It never retries a challenge, launches a browser, or accesses personal results, orders, or accounts. `--user-agent` is available if a different browser User-Agent is needed.

## Default city

Set Moscow as your default city:

```sh
invitro config set city moscow
```

Or set Kazan as your default city:

```sh
invitro config set city kazan
```

Choose one: only one default city is saved at a time. Running either command replaces the previous default and persists it across terminal sessions and working directories. You can then omit `--city` when searching or showing a product by ID:

```sh
invitro search ттг
invitro show 2467
```

Check your saved default:

```sh
invitro config
```

To use another city for one command without changing the saved default, pass `--city`:

```sh
invitro search --city moscow ферритин
invitro show --city kazan 2467
```

Use the site's lowercase city URL slug. There is no built-in city default. Precedence is `--city`, then a city in a product URL, then saved config. Product URLs without a city mean Moscow, matching the website. An explicit `--city` fetches the requested product's price in that city and returns its corresponding URL.

Config is stored at `os.UserConfigDir()/invitro/config.json`: `~/Library/Application Support/invitro/config.json` on macOS; normally `~/.config/invitro/config.json` on Linux. Writes are atomic with owner-only file permissions. Invalid updates preserve the previous file; malformed config is reported. `config` commands are entirely local. City existence is verified when a catalog command runs.

## Search and filters

```sh
invitro search ттг
invitro search --include-filters ттг
invitro search --page 2 витамин
invitro search ттг --available-at-home --deadline 1
invitro search витамин --is-promo
invitro search ттг --available-at-home=false
invitro search --type complexes щитовидная железа
invitro search --type checkups щитовидной
```

Use `--include-filters` to discover the current filters for the selected catalog type alongside your results. With Kazan saved as your default, run:

```sh
invitro search --include-filters ттг
invitro search --include-filters ттг | jq '.available_filters'
```

The extra `available_filters` object contains each filter's website label, control `type`, `is_active`, original `config`, and its `cli_flag` when supported by this CLI. For example, `available_filters.available_at_home.cli_flag` is `--available-at-home`. These are catalog-wide definitions, not counts for the current query. `config` preserves the site's JSON-encoded string, which may be `"{}"`.

Then apply the filters you want:

```sh
invitro search ттг --available-at-home --deadline 1 --include-filters
invitro search --type checkups --include-filters щитовидной
```

`--include-filters` only adds metadata; it does not enable filters or change the results. The existing `filters` object records filters you applied. Without the flag (or with `--include-filters=false`), `available_filters` is omitted. Fetching the definitions costs one additional HTTP request, including for empty search results. If that request fails, the command reports an error.

Each call fetches one page of up to 20 results in the API's order. `--type` selects `analyses` (default), `complexes`, or `checkups`. The catalog's filters are applied by Invitro before pagination:

| Flag | Meaning |
| --- | --- |
| `--available-at-home[=true\|false]` | Filter home collection availability |
| `--is-promo[=true\|false]` | Filter promotional items |
| `--deadline DAYS` | Maximum turnaround; `0` means today, `1` one day, `3` up to three days |

Checkups support `--is-promo` only, matching the captured website filter definitions. Omitted filters are not sent. False and omitted are distinct.

Output includes `total`, `count`, `page`, `total_pages`, `has_more`, `next_page`, `next_url`, `results`, `source_url`, `fetched_at`, and `http_requests`. Totals come from the filtered catalog response. An empty search has one empty page, `results: []`, `has_more: false`, and `next_page: null`. Requests beyond the last page fail. The CLI does not fetch subsequent pages automatically.

Each result contains:

- `id`: numeric website ID for an analysis/complex, UUID for a checkup.
- `catalog_id`: the backend UUID; `code` is the separately published test number.
- `type`, canonical `url`, `name`, and plain-text `description`.
- `price_from`, optional `old_price`, and `currency: "RUB"`.
- `loyalty_bonus`: published bonus accrual, or `null` if missing. This is cashback, not a deduction from today's price.
- `additional_services`: separately priced items such as blood collection.
- `deadline_days`, `is_weekdays_only`, `deadline_note`, `available_at_home`, and `is_promo`.

Missing numeric/boolean facts remain `null`; they are not converted to zero or false. Collection charges are separate from `price_from` and are not added into a guessed order total. The website states that the sample collection day does not count toward turnaround. Home collection availability does not specify a visit fee.

```sh
invitro search ттг | jq '.results[] | {id, name, price_from, additional_services, deadline_days}'
```

## Details

```sh
invitro show 2467
invitro show ce302550-a6c3-4525-9bcf-c48939edafa4
invitro show 'https://www.invitro.ru/analizes/for-doctors/kazan/629/2467/'
invitro show 69795 --type complexes
invitro show 2467 --full
```

Numeric IDs are **website IDs**, not the visible test codes (for example, website ID `2467` has code `199`). The public mapper resolves previously unseen numeric IDs, so a prior search is not required. UUIDs skip the mapper request. URLs determine the product type. For a previously unseen bare complex/checkup ID, pass `--type` or use its URL; bare IDs otherwise default to analyses. IDs returned by search remember their type automatically.

For Planeta-style scripts, `invitro show --url <product-url> <matching-id>` is also accepted. URLs must be HTTPS Invitro catalog URLs. Tracking queries and fragments are discarded. Requests always target the fixed public API origin, and returned product identities are checked against the requested ID.

Default output adds published sample `material`, analysis `method`, `categories`, and included `tests`, `services`, and `tags` for complexes/checkups, where available. `--full` additionally includes every nonempty `*_markup` instruction as plain text under `sections`, plus the complete response under `source`. This includes description, preparation, indications, interpretation, literature, and result information where published. Paragraphs, list items, and table cells retain boundaries; original HTML and links remain in `source`. Documents and linked products are not fetched.

The CLI returns published catalog facts and instructions; it does not interpret personal test results or recommend tests.

## Promotions and price comparison

```sh
invitro promos --city kazan
invitro promos '<slug-from-list>' --city kazan --full
invitro promos --city kazan --page 2
```

Replace `<slug-from-list>` with a slug returned by the listing. A promotion URL is also accepted. For a promotion found on a later page, pass that page's number to the detail command with `--page`.

`promos` reads the public city-filtered CMS behind the website's **Акции** page. It returns one page with `results`, `count`, `page`, `total_pages`, `has_more`, `next_page`, `source_url`, and freshness/request metadata. Offers have `title`, `description`, dates, and a public URL. New CMS pages also have a `slug` accepted by `promos <slug>`. Legacy pages (including loyalty and home visits) are exposed as links; their HTML/PDF terms are not fetched automatically. `api_total` preserves the upstream total, which may exceed the returned count after city filtering. Disabled, draft, future, expired and app-only promotions are excluded from the web list. Dates preserve the CMS timestamps; `null` means unspecified.

Supplying a slug or promotion URL checks that it is active in the city's selected listing page, retrieves its `sections` and `links` (including rules), and resolves each product card's article through the public `by-article` API. `offers[]` uses the **same product fields as search/show**: `id`, `catalog_id`, `type`, `url`, `name`, `code`, `price_from`, `old_price`, `currency`, `loyalty_bonus`, `additional_services`, and available composition/turnaround facts. It adds `article` and `promotion_title` for the landing page's label. Promotion-only products can have no website ID or category: their `id` is the catalog UUID and `url` is the promotion page. These products are not cached as ordinary products.

Some promotional articles have `is_promo: false` and never appear in `search --is-promo`. The CLI preserves that upstream flag; membership in `offers` records where the offer was found. A promotion can publish a separate article and price for a package. Use that offer's published price instead of applying the advertised percentage to an ordinary catalog price.

Composition may be published only in the landing page's `sections` rather than in the article's `tests`. Section names, block types, titles and plain text are retained in page order. No equivalence between tests is inferred. `--full` includes the untouched CMS response and each article response for scripts needing other structured fields. Unknown prices remain `null`; linked documents are available via `links`. City precedence matches show: explicit `--city`, URL city, then config.

The CLI supplies facts, **not an optimizer or an order total**. An agent or external script can combine them:

```sh
invitro search --city kazan холестерин > individual.json
invitro search --city kazan --type complexes липид > packages.json
invitro search --city kazan --type checkups липид > checkups.json
invitro show --city kazan --type complexes 6742 --full > package-detail.json
invitro promos --city kazan > promotions.json
invitro promos --city kazan '<slug-from-list>' --full > promotion-detail.json
jq '.offers[] | {code, name, price_from, additional_services}' promotion-detail.json
```

### Workflow for humans and LLM agents

1. Establish the requested city and required tests. Pass the same explicit `--city` to every command in a comparison.
2. Search individual analyses, complexes, and checkups. Follow `has_more` and `next_page` for each search and for the promotions listing; one page is not the complete catalog.
3. Fetch candidate products with `show --full` and candidate promotions with `promos <slug> --full`. Compare `tests`, `services`, and published composition in `sections`; preserve sample and preparation differences. Missing composition does not establish coverage or equivalence.
4. Read the linked promotion terms for eligibility, dates, participating offices, promo codes, and stacking conditions. Legacy promotion links require separate inspection. Keep unverified conditions explicit.
5. Calculate combinations in an external script or agent workflow using the returned JSON. Keep `null` prices unknown, use each offer's published price, and account for additional services once only where the order rules allow it. Some additional services are prerequisite analyses rather than collection fees; an empty list does not prove collection is free.
6. Report the selected products, covered tests, payable prices, additional fees, assumptions, source URLs, and `fetched_at` timestamps. Keep bonus accrual separate from the amount payable; the CLI does not access an account or its bonus balance.

The repository contains the Go CLI and Markdown usage instructions. Local comparison scripts and research output can be kept under `examples/` and `reports/`; both directories are ignored by Git.

Search and show remember only product identity, URL, and type under `os.UserCacheDir()/invitro/products/<city>/`. Prices and descriptions are fetched fresh on every invocation. The cache works across working directories and keeps cities separate.

## Request limits

| Command | Requests |
| --- | ---: |
| Search with matches, including filters and later pages | 3: city + search IDs + one catalog page |
| Search with no matching IDs | 2: city + search IDs |
| Search with `--include-filters` | One additional request for filter definitions: 4 with matching IDs, 3 without |
| Show a numeric ID or numeric product URL | 3: city + ID mapper + details |
| Show a UUID or UUID checkup URL | 2: city + details |
| Config, help | 0 |
| Promos list | 2: city + one CMS listing page |
| Promos detail | 3 + number of distinct product cards (maximum 20): city + listing + CMS page + article prices |

Catalog commands stop at three requests, or four for search with `--include-filters`; promotion details stop at 23. Redirects, retries, and automatic pagination are disabled. `--timeout` defaults to 90 seconds for the entire command. The search API supplies its matching ID list in one response; the CLI fetches details for only the requested result page. Response bodies are limited to 16 MiB.

## Verification

```sh
go test -race ./...
moon run invitro:lint
go build .
```

Tests use recorded public JSON and local HTTP servers; they do not need Chrome, credentials, or network access. They cover command compatibility, flag placement, config and cache behavior, URL validation and identity checks, city precedence, filters, pagination, empty results, price/fee separation, full instructions, malformed responses, cancellation, and bounded requests. GitHub Actions runs the same checks.

## License

[MIT](LICENSE). Command parsing and local-state handling are adapted from Planeta.
