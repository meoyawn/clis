# Glassdoor salary CLI

- Read-only salary research through the observed native website contracts. Keep base pay and total pay separate; preserve source currency, period, sample size and scope.
- Never replay telemetry mutations or submit salaries, reviews, applications or profile changes. Keep captures and credentials outside Git.
- One foreground command per account at a time. Reuse HTTP connections within that process and close them on exit; no daemon or connections retained between processes.
- Stop on authentication, access challenges and rate limits. Do not add automatic retries.
