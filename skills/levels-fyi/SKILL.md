---
name: levels-fyi
description: Research global remote compensation and country comparisons with the local Levels.fyi Go CLI.
---

Run from the local `github.com/meoyawn/clis` checkout (usually `~/Developer/clis`): `go run ./levels-fyi --help`.

- `go run ./levels-fyi auth login --browser chrome --profile Default` imports the selected Chrome profile using SweetCookie, including its actual Cognito idToken. Tokens stay local; an expired token requires refreshing the site and importing again.
- Example: `go run ./levels-fyi salaries estimate --role golang --level senior --remote`.
- No country IDs means all available countries, including US. Remote is a real work-arrangement filter. Discover native country IDs through `search locations --query CITY` (Kazan returns Russia's ID 203).
- Role is broad native text search, not an exact language classifier. Senior means the native 7+ years band. Company is an optional exact slug.
- Default: past 365 days, at most five sequential 50-row requests, sanitized report cache for 6 hours. `--refresh` bypasses cache; `--max-pages` bounds requests. No daemon or background connection persistence.
- Native amounts are annual USD even when original-currency metadata says otherwise. Preserve source FX metadata; keep base, stock, bonus and total separate, grouped by gross/net and employment type, with per-country coverage.
- Sample percentiles describe retrieved recent submissions, not a representative world population. The search total may be capped at 250; never call it the full database size or an employer budget. Sparse Go-specific results need other comparable evidence.
- Per-field anonymity markers remain null, with original experience ranges preserved; account access gates still stop the command. Respect normal account access; stop on blocks or rate limits without retries. This CLI does not submit salaries or profiles. Attribute evidence to https://www.levels.fyi/ and preserve exact query URLs and retrieval time.
