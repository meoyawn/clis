---
name: glassdoor
description: Research salary estimates by role, experience and location with the local Glassdoor Go CLI.
---

Run from the local `github.com/meoyawn/clis` checkout (usually `~/Developer/clis`): `go run ./glassdoor --help`.

- `go run ./glassdoor auth login --browser chrome --profile Default` imports a real Chrome profile through SweetCookie; `auth status` does not test online authentication.
- Use `search titles`, `search locations`, `search companies` and `salaries filters` for actual suggestions and industries. Catalogs cache for 24 hours; ambiguous identities require a native ID.
- Example: `go run ./glassdoor salaries estimate --role golang --level senior --location Russia`.
- `--level senior` searches the native Senior job title; `--experience 7-9` is a separate native experience filter. Never equate them silently.
- Removing location defaults to the US. Native Remote is also a US location, not a world sample. Use Levels.fyi's global work-arrangement query for worldwide remote evidence; do not relabel US figures.
- Every monetary percentile has amount, currency and interval. Keep base/additional/total separate, preserve source update date, sample count and provider confidence. Zero submitted salaries may accompany a provider estimate; it is not observed cohort pay.
- Native payPeriod overrides contradictory FAQ wording. Gross/net remains unknown. Employer autocomplete proves neither identity nor hiring budget.
- Stop on blocks, rate limits or changed response shapes without retry. One foreground process, no daemon or telemetry mutations.
