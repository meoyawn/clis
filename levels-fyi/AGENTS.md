# Levels.fyi salary CLI

- Read-only salary research through observed native contracts. Never submit salaries, profiles, applications, telemetry or reviews from this CLI.
- Import the selected Chrome profile with SweetCookie; do not log cookies, JWTs, captures or account fields. Keep fixtures synthetic.
- One foreground command per account. No daemon and no connection persistence between processes; close idle connections on exit.
- Stop on authentication, access challenges, redactions and rate limits; no automatic retries or paywall bypasses.
- Before the first API request, an expired saved token may be resynced once from the already-selected Chrome profile using SweetCookie. Preserve the same account and private session-file permissions. This local read is not a network token refresh or a retry; if Chrome is also expired or its account changed, stop with actionable instructions.
- Preserve sample coverage, countries, dates, gross/net and employment type. Retrieved recent submissions are not a representative global population or an employer budget.
- `--remote` filters reported work arrangement; no country IDs means reports across available countries, not jobs that hire worldwide or location-independent pay. Make this distinction in help, errors and fresh/cached JSON. Verify worldwide hiring/pay scope separately before treating a cohort as a global hiring benchmark; do not delete US rows to manufacture a cheaper benchmark.
- With `currency=USD`, native search amounts are already annual USD even when original-currency metadata differs. Preserve them without another FX or hours multiplier. Keep base, stock, bonus and total separate; source FX dates and weekly hours remain unknown.
- P50/P90 describe the retrieved sample and the selected pay component, not advertised-band midpoints or a recommended ask. Scope and representativeness must be established before using them as a market benchmark.
