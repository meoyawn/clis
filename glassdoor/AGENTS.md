# Glassdoor salary CLI

- Read-only salary research through the observed native website contracts. Keep base pay and total pay separate; preserve source currency, period, sample size and scope.
- Require an explicit verified location. Native `Remote` is a US location, not worldwide hiring. This API has no worldwide-hiring cohort; Levels.fyi's all-country remote sample does not verify one either. State invariant limitations in help, errors and docs; JSON retains the actual location and sample data.
- Percentiles are provider estimates for the selected title/location/experience, not automatically representative market percentiles. Provider confidence does not establish fit, worldwide hiring or gross/net basis. Never infer annual units or tax basis from generic page text; use the native pay-period field and keep unknowns explicit.
- Never replay telemetry mutations or submit salaries, reviews, applications or profile changes. Keep captures and credentials outside Git.
- One foreground command per account at a time. Reuse HTTP connections within that process and close them on exit; no daemon or connections retained between processes.
- Stop on authentication, access challenges and rate limits. Do not add automatic retries.
- Session imports require a safe native request-header snapshot from the same running Chrome profile as the cookies. Never derive authenticated headers from the installed app version: Chrome can update on disk before the running process restarts.
- On a confirmed human-verification challenge, print the fixed Glassdoor salary-page verification URL and exit nonzero. The CLI must not launch a browser or retry. Follow the Glassdoor skill to open the printed URL in Chrome, complete verification, reimport the same Chrome profile with fresh native headers, and resume the original CLI command once. An ordinary 403 or 429 alone must not trigger this recovery flow.
