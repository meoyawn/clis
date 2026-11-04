# habr-career

Habr Career's native profile and applications, with an HH-shaped command tree.

```sh
go run . --help
go run . auth import
go run . auth status
go run . resumes list
go run . resume view YOUR_ALIAS
go run . vacancy get https://career.habr.com/vacancies/1000000000
go run . application prepare 1000000000 --resume YOUR_ALIAS --text-file letter.txt --output draft.json
go run . application submit --draft draft.json
```

Only `auth import` accesses Chrome through SweetCookie. `auth status` is local;
workflow commands use the saved owner-only jar. An available cookie is not proof
of server-side authentication. The skill and `--help` document the workflow.

Submission is a deliberate write under the user's authorization, never a tooling
test. After reviewing the package, `application submit --draft draft.json --send
--approved SHA256` creates a response, verifies its identity, saves the letter,
and independently verifies the exact text. Habr's native flow has two writes:
failure after creation can leave an application without a letter. Inspect
`application get`/`application letter` before any recovery; never blindly repeat.

Unlike HH, Habr has a single shared native profile, selected by stable account
alias. There are no profile edits, resume uploads, salary changes, messaging,
paid-service operations or external-ATS submissions. Optional salary is left
untouched because Habr saves it to the shared profile. Employer pay and Habr's
salary prediction are kept separate.

Native contracts were inspected on 2026-11-04: vacancy/profile SSR state,
`/api/frontend_v1/users/me`, the profile's explicit `/ALIAS/print` link, and the
vacancy page's bundled response adapter (`POST /api/frontend/vacancies/ID/responses`,
then `PATCH .../RESPONSE_ID` with multipart `body`). Preparation performs GET only.
No quick-response request is used for inspection. Auth redirects, protection,
rate limits and unknown forms stop without retries. Captures and account data
must stay outside Git. Use `har inspect` for offline schema summaries; use Chrome
only to discover missing HTTP contracts, implement/test support, then use the CLI.

```sh
moon run habr-career:check
```
