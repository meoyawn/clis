# Getmatch salary CLI

- Read-only salary research; use observed native endpoints and preserve units, sample sizes and provenance.
- Never output personal/profile salary fields, cookies or session tokens. Keep fixtures synthetic and captures outside Git.
- One foreground command per account at a time. Reuse HTTP connections within that process and close them on exit; no daemon or connections retained between processes.
- Stop on authentication, access challenges and rate limits. Do not add automatic retries.
