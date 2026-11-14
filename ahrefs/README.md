# Ahrefs

Read Ahrefs Site Audit and Site Explorer through the signed-in Chrome account using SweetCookie.
Run from the workspace root:

```sh
go run ./ahrefs --help
go run ./ahrefs auth import
go run ./ahrefs auth status
go run ./ahrefs access
go run ./ahrefs organic-keywords example.com --country us --limit 100
go run ./ahrefs projects
go run ./ahrefs crawls PROJECT_ID
go run ./ahrefs issues PROJECT_ID --crawl CRAWL_TIMESTAMP
go run ./ahrefs issue PROJECT_ID ISSUE_ID
go run ./ahrefs pages PROJECT_ID --crawl CRAWL_TIMESTAMP --issue ISSUE_ID --all
```

`issues` also accepts a complete All issues URL. `pages` accepts complete Data
Explorer links from Ahrefs or crawl notification emails, preserving their crawl,
issue, saved filter and columns. Quote URLs in your shell. Explicit flags take
precedence over URL values; unsupported URL parameters fail instead of silently
changing the view. Both Open Graph issues and meta description issues use this
same workflow, with their own filters and columns supplied by Ahrefs.

The default crawl is the newest completed crawl, compared with its preceding
completed crawl. Specify `--compare-with none` to disable comparison. Issue
summaries default to active issues with affected pages; `issues --all` includes
every tracked issue. `issue` includes the explanation and suggested fix.

`pages` returns one server page by default, with `total`, `offset` and an optional
`next_offset`. `--all` collects every remaining page. Column names match Ahrefs;
lists remain arrays, null stays null, and numeric zero and false stay intact.
Open Graph attributes and values align by index and include the entire lists.
The command's `--timeout` covers every request, including pagination.

`auth import` selects Chrome's `Default` profile unless `--browser-profile` is
supplied. It uses SweetCookie to save only Ahrefs session and protection cookies
to an owner-only jar in the OS config directory under `ahrefs/session.json`.
`auth import --cookies-file` is an explicit SweetCookie JSON fallback.
`--session-file` overrides the jar location for any command.

Workflow commands and `auth status` read this jar directly, without Chrome or
Keychain access. Status only reports local cookie availability, not validity
with Ahrefs. Cookie values are never included in output. Sign in manually and
run `auth import` again when the session expires; workflow commands never
automatically import or refresh browser credentials.

The client uses the read-only `/v4/saProjects`, `saCrawls`,
`saGetProjectIssues`, `saGetDiffsByIssues`, `saGetProjectIssueById`, and
`saDeTable` requests observed in Chrome. These web endpoints use POST without
starting crawls or changing settings. No API key or paid export is needed.
Redirects, HTTP errors, API errors and non-JSON login/protection responses stop
without retries. Resolve authentication or rate limiting before rerunning.
Only Simple fields in the Pages table are currently supported.

Tests use synthetic responses and cookie values. Do not commit browser captures,
credentials, private exports or account data.

```sh
moon run ahrefs:check
```
