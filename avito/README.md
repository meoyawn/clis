# avito

A Go CLI for Avito search, filter discovery, and listing details, using browser cookies imported with [sweetcookie](https://github.com/steipete/sweetcookie). Auth commands show a short, readable sign-in result; search, filters, show, and config emit JSON. Errors go to stderr and exit nonzero.

The web protocol follows observed Avito responses. Parser and command tests use synthetic fixtures with the same structure, without publishing private captures or real listing data. Authentication, city lookup, filter discovery, brand-value lookup, filtered search, pagination, and listing details have been checked live. Sustained request limits have not been verified.

## Install

Requires Go 1.27.2 or newer. Install the latest published version:

```sh
go install github.com/meoyawn/clis/avito@latest
avito --help
```

Or run from this CLI directory:

```sh
go run . --help
```

Go installs into `GOBIN`, or `GOPATH/bin` when `GOBIN` is unset. That directory must be on your PATH. To keep the executable in this checkout:

```sh
go build -o bin/avito .
```

## Import authentication

```sh
avito auth import --browser chrome
# Signed in as Алексей Пример. Session saved.
avito auth status
# Signed in as Алексей Пример.
```

The name above is an example. Both commands check Avito's current profile and show the account name returned by Avito. Success means the session was verified. Missing or rejected sessions, browser checks, connection failures, and unexpected responses exit nonzero with a short explanation and a next step. An unavailable check is not treated as a signed-out account.

Supported browsers: `chrome`, `firefox`, `brave`, `edge`, `safari`. The browser must have an existing Avito session. Select another profile when needed:

```sh
avito auth import --browser chrome --browser-profile 'Profile 1'
```

Import reads the selected browser locally through sweetcookie; OS Keychain/keyring access may be needed. It keeps only valid, unexpired cookies for the Avito web origin and the paths used by auth, city lookup, search, filter-value lookup, and show. It makes one read-only request to `/web/1/profiles/list?limit=3&withEntities=true`, verifies the explicitly current profile, and then saves the session. Failed imports, including failed verification, preserve the previous store. Search/show subsequently read that store; they do not reopen the browser cookie database or silently re-import credentials. To refresh a session, run `auth import` again after signing in to Avito in the browser.

`auth status` checks the saved session with the same profile request, without reading the browser or changing the saved session. Missing or fully expired credentials fail locally. Both auth commands accept `--timeout 60s` and `--user-agent '…'`.

Use `avito auth status --json` or `avito auth import --browser chrome --json` for scripts and cookie diagnostics. JSON includes the verified account, `is_authenticated`, `login_verified`, check time, browser/profile, import time, cookie names and counts, and expiry information, without cookie values. When a check fails, JSON includes an `error` and the command still exits nonzero. An omitted `is_authenticated` means the account could not be checked. `login_verified` records whether Avito gave a definitive signed-in or signed-out result. Search/show include Avito's `is_authenticated` flag when present and warn if saved cookies produce a logged-out response.

The credential file is `avito/auth.json` inside Go's user config directory: `~/Library/Application Support/avito/auth.json` on macOS, or `$XDG_CONFIG_HOME/avito/auth.json` (default `~/.config/avito/auth.json`) on Linux. It contains decrypted cookie values and is written atomically with mode `0600` in a `0700` directory. Import/status output never includes those values. Browser session cookies with no expiry remain stored until replaced; the profile check verifies their current server-side validity.

## User config

```sh
avito config
avito config set city Казань
avito config set local-priority true
avito config set timeout 60s
avito search 'кокосовое масло'
```

`avito config` prints all effective user preferences as JSON. `avito config set <key> <value>` saves a preference and prints the updated config. Viewing config and setting `local-priority` or `timeout` are local. Setting `city` resolves an uncached city once through Avito using saved cookies; cached cities require no request. A missing file uses these defaults:

| Key | Default | Purpose |
| --- | --- | --- |
| `city` | Unset (`""`) | Set a default city, or pass `--city` for each search |
| `local-priority` | `true` | Ask Avito to show the selected city first |
| `timeout` | `60s` | Timeout for search, show, and auth commands |

Preferences are saved in `avito/config.json` beside `auth.json`: `~/Library/Application Support/avito/config.json` on macOS, or `$XDG_CONFIG_HOME/avito/config.json` (default `~/.config/avito/config.json`) on Linux. Writes are atomic, with a `0600` file in a `0700` directory. Config contains only the city name. Numeric city IDs are kept separately under the user cache directory (`~/Library/Caches/avito/locations` on macOS, `$XDG_CACHE_HOME/avito/locations` or `~/.cache/avito/locations` on Linux) and reused across commands. City names and IDs are never built into the client. Category, page, limit, and User-Agent are command options and are not config keys.

Explicit flags override the saved preferences for that invocation. Unknown keys, invalid values, and failed city lookups exit nonzero and preserve the saved config. Malformed config files are reported instead of silently reverting to defaults.

## Filters

```sh
avito filters 'холодильник'
avito filters 'холодильник' --only price
```

`filters` requests one search response and exports the current category path, category choices, filter keys, option labels and values, selected values, range input keys, and sorting choices as JSON. It uses the same city config and request flags as search. A query is required unless `--category-id` is supplied. Category and attribute IDs come from Avito; none are built into the CLI.

Copy the returned `search_args` array into subsequent `filters` or `search` calls. It contains flags only, including location, category, hidden category parameters, active filters, and sorting. Pass the query separately. **Retain the hidden parameters when refining a search:** adding explicit filters can stop Avito from inferring the fridge subcategory. To change a selection already in `search_args`, replace that key's existing values rather than appending a second conflicting value.

- Pass an option as `--filter 'KEY=VALUE'`. For `multiple: true`, repeat the key for each selected value. The CLI encodes multiple values as an indexed array. Quote keys containing brackets.
- For `numericRange`, use the keys in `inputs`, such as `--filter pmin=1000 --filter pmax=5000`. Range bounds and keyword text remain separate inputs.
- For `keywords`, pass text as the value. Fridge search currently exposes description keywords and new/used condition; cooling faults and exact age require checking the listing text and `show` attributes.
- `updates_filters: true` indicates dependent fields: rerun `filters` with the selected brand to discover its model options. `--only KEY` limits the printed form to one field without an extra request.
- Use `sorting[].value` with `--sort`. Fields with `supported: false` describe UI controls the CLI cannot submit; their nested supported fields are listed separately.

Seller rating, delivery, returns, and self-pickup are discovered through this same form, including nested checkboxes. For example, the live skateboard form exposes these selections (rediscover them for other categories):

| Selection | Argument |
| --- | --- |
| Seller rating at least 4.7 | `--filter 'params[196498]=3370585'` |
| Avito Delivery | `--filter withDeliveryOnly=1` |
| Returns available | `--filter 'params[149578]=1'` |
| Avito self-pickup | `--filter 'params[186205]=1'` |

Retain the category and hidden parameters from `search_args` when adding these selections. Delivery, returns, and self-pickup are separate filters; enabling delivery does not imply returns. The rating checkbox uses Avito's offered threshold, not an arbitrary numeric minimum. Check each result's `seller.rating` and `seller.num_ratings` for a stricter threshold or minimum review count. Seller descriptions can contradict their delivery attributes, so check the full listing before ordering.

`has_suggest: true` means the form's options may be incomplete. Search that field directly with `filters --field 'params[ID]' --value-query TEXT`, copying `search_args` from discovery to retain the category and selections. This mode calls `/web/2/suggest/desktop` once and does not fetch the form first. Omit `--value-query` to list options. It returns `options`, `total`, `count`, `has_more`, and `next_offset`; request another page explicitly with `--offset N`. `--field` requires a category and cannot be combined with `--only`.

For example, a script can discover a brand and use its actual key/value without knowing any IDs beforehand:

```python
import json
import subprocess

def avito(*args):
    return json.loads(subprocess.check_output(["avito", *args]))

form = avito("filters", "холодильник")
brand = next(f for f in form["filters"] if f["title"] == "Производитель")
beko = next(o for o in brand["options"] if o["label"].casefold() == "beko")
args = [*form["search_args"], "--filter", f'{brand["key"]}={beko["value"]}',
        "--filter", "pmax=5000"]
result = avito("search", form["query"], *args, "--limit", "0")
print(json.dumps(result, ensure_ascii=False))
```

If the option is absent from the initial form, use its suggestion lookup. A low price cap is a starting point for finding faulty units, not evidence of a fault. Check full descriptions and attributes with `show`, exclude working units, parts, bundles, and other cities as appropriate, and compute statistics from the remaining comparables. Listing prices are asking prices, not completed sales.

## Search

```sh
avito search 'кокосовое масло' --city Казань
avito search 'телевизор'
avito search 'телевизор' --city Москва --category-id 32
avito search 'телевизор' --page 2
avito search 'телевизор' --limit 0
avito search 'телевизор' --location-id 650400
avito search 'холодильник' --filter pmax=5000 --sort 1
```

Search uses the explicit `--city` or saved city preference and calls `/web/1/js/items` using saved browser cookies. It reuses cached city IDs. An uncached city needs one lookup through `/web/1/slocations?q=…&limit=10` before searching, and the resolved ID is saved internally for later commands. City matching requires a full Russian name and ignores letter case. Unknown or ambiguous names stop before the search. With no saved city, pass `--city` or `--location-id`; an unspecified location fails locally. `--location-id ID` overrides the saved city and bypasses city resolution. `--city` and `--location-id` cannot be combined. Flags can appear before or after the query. Use `--` before a query beginning with a dash.

The search request sends the resolved `locationId` and, by default, `localPriority=1`, matching Avito's “Сначала в выбранном городе” selection in the city capture. Avito controls the ordering and may include listings from other cities. The CLI preserves the returned order and does not filter listings by city. Use `--local-priority=false` to send `localPriority=0` for one search, or `avito config set local-priority false` to save that preference.

Each invocation requests exactly one page. Results include public listing fields, canonical URLs, `total`, `accessible_total`, actual listing `count`, `applied_filters`, reusable `search_args`, and pagination metadata. The search fixture exercises 50 listings, `total: 5887`, and `accessible_total: 1500`. These are result counts, not request quotas. `--page` sends Avito's `p` parameter; page 2 has been verified live. The parser rejects a response with the wrong page, location, explicit category, filter selections, or sort. It does not automatically fetch subsequent pages.

Every listing includes `seller.rating` and `seller.num_ratings`, with `null` for unavailable values. The public seller name and original `ratings_summary` are included when supplied. Search extracts exact counts from summaries such as `123 отзыва` or `1 234 отзыва`; rounded or unrecognized summaries keep `num_ratings: null`. Missing ratings are never reported as zero. These fields require no extra requests. For example:

```json
"seller": {
  "name": "Тестовый продавец",
  "rating": 4.9,
  "num_ratings": 123,
  "ratings_summary": "123 отзыва"
}
```

`accessible_total` caps Avito's accessible count at the matching total. Avito can include broader recommendations in its raw `totalCount`; `similar_total` reports those separately, and they do not extend search pagination. The CLI excludes separate recommendation grids, cards marked `isSimilarToSearch`, and cards with missing or out-of-range displayed prices when price bounds were requested. `excluded_count` and warnings explain excluded cards from the main grid; server totals still describe the untrimmed results. Avito's declared filters cannot establish that every seller's text agrees with their attributes, so confirm ambiguous comparables with `show`.

Use `--limit N` to print at most the first N listings from the requested page. The default is 10; `--limit 0` prints the full page and negative limits are rejected. `count` reflects the printed listings, while `total`, `accessible_total`, `has_more`, and `next_page` still describe the server's pagination. A limit larger than the page returns only the available listings and does not fetch another page. Category and pagination start at no explicit category and page 1 on every invocation.

Optional request flags: `--timeout 60s` and `--user-agent '…'`. Requests send Avito's JSON request headers, use a configurable Chrome User-Agent, and use Go's HTTP/TLS transport; cookie import does not reproduce Chrome's transport or execute browser challenges.

## Show

```sh
avito show '<listing-url-from-search>'
avito show '<listing-id-from-search>'
```

Show fetches the listing HTML and reads its embedded public listing data without running JavaScript. Output includes description, price, images, location, attributes, and active status when present. Replace the placeholders above with an actual URL or ID returned by search.

Show includes the same `seller.rating` and `seller.num_ratings` fields as search, preferring the listing page's numeric review count over its formatted summary. Unknown values remain `null`; only public seller fields are exported.

**An ID needs a canonical URL previously seen in web search or a successful show.** For an unseen ID, provide the full listing URL. This follows Planeta's local ID-to-URL approach and avoids extra lookup requests. The index stores only ID/URL pairs under the user cache directory (`~/Library/Caches/avito/items` on macOS); responses and prices are fetched again on every call. Tracking parameters are removed from stored URLs. `avito.ru` and `m.avito.ru` listing URLs are normalized to HTTPS `www.avito.ru`.

## Request and error contract

- Search, filter discovery, and explicit filter-value lookup each make one request. An uncached city adds one lookup; cached cities and `--location-id` require none. `config set city` makes at most one lookup and saves the mapping for subsequent searches; other config commands make no requests. Auth import/status and show each make one request. All requests use the saved session, and the timeout covers city lookup and the main request together. Invalid input or missing or unusable credentials fail before an outbound request.
- HTTP 429 exits nonzero and reports `Retry-After` exactly when supplied, including either seconds or an HTTP date. No hint is invented when absent.
- Retries, pacing, concurrency, and retry scheduling belong to the caller. There are no shared cooldowns, sleeps, locks, or rate-limit state files.
- There is no automatic pagination, redirect following, engine fallback, or response cache. A redirect requires the current canonical URL.
- Requests use the direct connection, without environment HTTP proxies. A challenge, unexpected HTML/JSON, wrong listing ID, or changed response schema is an error rather than an empty successful result.
- Search/filter/show metadata includes `http_requests` (normally 1; 2 if search or filters needed a city lookup), fetch time, source URL, and any supplied rate-limit headers from the result request. A failed city lookup stops the command before its main request. Auth JSON includes only the current account's ID/name and safe diagnostics; cookies and complete current-user page state are excluded.

The captures do not establish a safe requests-per-minute value or sustained reliability. The CLI uses the web flow exclusively.

## Validation

From this monorepo, run:

```sh
moon run avito:check
# Or separately:
moon run avito:test
moon run avito:lint
moon run avito:build
```

Moon's Go toolchain plugin installs the pinned Go and golangci-lint versions.
All CLIs share the root `.golangci.yml`. Checks run race tests, lint, and a build;
GitHub Actions runs `moon run :check` for every pull request and push.

Tests exercise web search JSON, city lookup and server-side priority parameters, persistent preferences, internal city ID reuse without repeated HTTP calls, flag overrides, config permissions and invalid updates, listing HTML, readable auth results, opt-in JSON, current-profile verification, rejected/unknown sessions, private-store permissions, failed-import preservation, cookie scope and expiry, secret-free JSON, URL indexing, and request counts on success and failure. They use synthetic response fixtures and credentials/account identities; they do not contact Avito or extract real browser cookies. Raw HARs are excluded from the repository because page state can contain account data even when cookie headers have been sanitized.

Filter tests cover category selection, hidden parameters, brand/model dependencies, nested controls, ranges and keywords, scalar/array selections, explicit option pagination, rejected or ignored selections, displayed price bounds, recommendation exclusion, and request counts. To replay a private capture locally without network requests:

```sh
AVITO_FILTERS_HAR=/path/to/avito-filters.har go test ./internal/service -run TestPrivateFilterHAR -v
```

A fresh `codex exec` run with GPT-6 Astra at `xhigh` also exercised the compiled CLI using only its help and commands. It discovered the filters, screened 11 listings, fetched four details, and identified two matching cooling-fault comparables in seven HTTP requests. It kept age uncertainty separate from the price estimate. The live dataset and execution transcript remain outside the repository.
