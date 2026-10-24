# Synthetic response fixtures

All listing text, account names, account IDs, listing IDs, descriptions, and image URLs in these fixtures are synthetic. They preserve the response structure needed by the parser tests without publishing captured personal information. Geographic names and Avito location IDs are public reference values.

- `web_search.json`: 50 synthetic listing rows using the observed web search schema. Aggregate counts and pagination fields exercise result parsing. The first listing is `1000000001`; listing `1000000002` is also used by the show tests.
- `web_show.html`: a synthetic listing wrapped in Avito's `window.__staticRouterHydrationData = JSON.parse(...)` format. It includes price, images, HTML paragraphs, location, and four attributes. The email at `example.invalid`, current-user ID, and `SYNTHETIC_SECRET_DO_NOT_PRINT` nonce are deliberate leak-detection canaries.
- `web_profiles.json`: a synthetic current-account response matching the observed `GET /web/1/profiles/list?limit=3&withEntities=true` schema. The name, title, and profile ID are invented.
- `web_locations.json`: a minimal response in the observed `GET /web/1/slocations` format, containing Kazan and its fallback region choices. It includes only public geographic names and IDs. The city HAR also establishes that “Сначала в выбранном городе” sends `localPriority=1` alongside `locationId=650400` in the search request.
- `web_filters.json`: a synthetic fridge search with the observed `filtersV2.Sections` form, category hierarchy, hidden category parameters, brand/model dependencies, range input IDs, nested fields, and keyword controls. Listings and prices are invented; public attribute IDs and option values model the captured protocol. Two deliberately unsuitable cards exercise price and recommendation exclusion.
- `web_filter_values.json`: a synthetic `GET /web/2/suggest/desktop` page, including selected options, duplicate popular/all entries, and an explicit next offset.

The example image URLs are parser inputs and are never fetched by the tests. Raw browser captures and experiment responses are excluded from the repository.

To replay a private filter capture locally without committing it, set `AVITO_FILTERS_HAR` and run `go test ./internal/service -run TestPrivateFilterHAR -v`. The test reads search/filter and suggestion response bodies and checks selections against the captured request parameters. It makes no HTTP calls and does not log headers or cookies.
