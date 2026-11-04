# habr-career

Habr Career's single native profile and applications.

```sh
go run . --help
go run . auth import
go run . auth status
go run . profile get
go run . profile specialization get
go run . resume view YOUR_ALIAS
go run . vacancy get https://career.habr.com/vacancies/1000000000
go run . applications list
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
`application verify --draft draft.json` verifies an existing application without
writing. Only terminal CR/LF differences are accepted: Habr strips the letter's
final newline. Normalization is reported together with the exact submitted text;
all other content differences fail verification.

Unlike HH, Habr has a single shared native profile, selected by stable account
alias. There is no plural resume-list or clone command: `profile get` returns
the account alias used for ownership checks, not a choice of resume variants.
`profile specialization update --patch FILE` previews a headline/language edit;
`--apply` backs up and preserves the other specialization form fields, reconstructs
the native widgets, posts paired language/grade rows in Rails form order, and
verifies the form, public profile and employer print view. Omitted patch keys
stay unchanged. `profile specialization verify --patch FILE` is read-only recovery.
`resume experiences` lists native row IDs; `resume experience` exports one
exact edit form. `resume update --experience ID --description-file FILE` previews
an HTML description replacement; `--apply` backs up and writes the fresh native
form, preserving repeated fields and checking the saved form, employer print
view and unrelated profile/employment content. `resume verify` is GET-only.
Date, current-employment, specialization and skill widgets are reconstructed
from the fresh native SSR state; missing or conflicting widget state stops the
update before a write. Static form controls alone are not a complete payload.
`application update --resume ALIAS --text-file FILE` previews a replacement
letter for an existing response; `--apply` backs up the original, PATCHes only
its body and verifies the same response ID and text. Neither update creates an
application or sends a separate message. All writes require user authorization.
There are no resume uploads, salary/contact changes, paid-service operations
or external-ATS submissions. Optional salary is left
untouched because Habr saves it to the shared profile. Employer pay and Habr's
salary prediction are kept separate.

Native contracts were inspected on 2026-11-04: vacancy/profile SSR state,
`/api/frontend_v1/users/me`, the profile's explicit `/ALIAS/print` link, and the
vacancy page's bundled response adapter (`POST /api/frontend/vacancies/ID/responses`,
then `PATCH .../RESPONSE_ID` with multipart `body`). Preparation performs GET only.
Employment editing uses the account's actual `/profile/experiences/ID/edit` form
and its inspected AJAX contract: URL-encoded POST with `_method=patch`, fresh
CSRF protection and the current metadata. The JavaScript response is never
executed; independent form and print reads establish whether the save worked.
`applications list` reads the native `/responses` table and follows its own
pagination links, checking the account and advertised total; deleted responses
are excluded. Read status is the site's label, not inferred interview progress.
No quick-response request is used for inspection. Auth redirects, protection,
rate limits and unknown forms stop without retries. Captures and account data
must stay outside Git. Use `har inspect` for offline schema summaries; use Chrome
only to discover missing HTTP contracts, implement/test support, then use the CLI.

```sh
moon run habr-career:check
```
