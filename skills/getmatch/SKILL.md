---
name: getmatch
description: Research Russian engineering salary benchmarks with the local Getmatch Go CLI.
---

Run from the local `github.com/meoyawn/clis` checkout (usually `~/Developer/clis`): `go run ./getmatch --help`.

- `go run ./getmatch auth login --browser chrome --profile Default` imports the selected Chrome session through SweetCookie; `auth status` is local metadata only.
- Discover native specialization slugs with `salaries filters`; public catalogs cache for 24 hours.
- Example: `go run ./getmatch salaries estimate --role golang --level senior --remote --company Surf`.
- Company lookup covers only the returned top ranking, not all company reports. Missing Surf is not proof of no pay data or a budget estimate.
- Preserve source URLs, retrieval date, monthly RUB and unknown gross/net. Chart percentiles and histogram counts describe the selected filters; the stats endpoint (bonus, market summary, company ranking) can use a broader cohort. Keep those scopes separate. `--hours-per-month` is explicit arithmetic, not a contractor quote.
- Personal/profile salary fields are excluded. A partial or blocked report stops; no automatic retry or session import. One foreground command, no daemon.
