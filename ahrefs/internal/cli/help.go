package cli

const help = `ahrefs — Site Audit through your signed-in Chrome account; JSON output

Workflow:
  ahrefs auth import [--browser-profile Default]
  ahrefs auth status
  ahrefs projects
  ahrefs crawls PROJECT
  ahrefs issues PROJECT_OR_URL [--crawl STAMP] [--level LEVEL] [--search TEXT]
  ahrefs issue PROJECT_OR_URL ISSUE_ID
  ahrefs pages PROJECT_OR_URL [--issue ISSUE_ID] [--all]

Start with projects, choose a project and completed crawl, then list issues.
Use issue for Ahrefs' explanation, suggested fix, filter ID and page columns.
Pass an issue ID to pages to use that issue's exact filter and default columns.
issues accepts the All issues URL. pages accepts a complete Data Explorer URL,
including current, issueId, filterId and columns; quote URLs in the shell.
Unknown URL parameters are rejected so a view is never silently broadened.

Crawl options (issues/pages):
  --crawl STAMP          latest (default), RFC3339, or DD-MM-YYYYTHHMMSS
  --compare-with STAMP   Compare with this earlier completed crawl, or none
                        Default: the completed crawl immediately preceding it
                        Explicit flags override the corresponding URL values

issues:
  --all                 Include zero-count and turned-off tracked issues
                        Default: active issues with at least one affected page
  --level LEVEL         error, warning, or notice
  --search TEXT         Case-insensitive substring of the issue name
                        Counts and available changes come from Ahrefs
                        Null change counts remain null; zero remains zero

pages:
  --issue ID            Use an issue's page filter and default columns
  --filter-id ID        Explicit saved page filter; overrides the issue filter
  --columns CSV         Ahrefs field names; overrides URL/issue columns
  --sort FIELD[:ORDER]  Default pageRating:desc; ORDER is asc or desc
  --limit N             Rows per request, 1–1000 (default 50)
  --offset N            Start at this zero-based row offset (default 0)
  --all                 Fetch every remaining page from offset
                        Otherwise next_offset identifies the next server page

Common page columns: pageRating,url,traffic,httpCode,depth,compliant,
ogTagsValid,ogTagsAttributes,ogTagsValue,metaDescription,
metaDescriptionLength,nrMetaDescription. Use issue for issue-specific columns.
Column names are preserved in rows. Lists stay arrays, including all Open Graph
attributes/values and multiple meta descriptions. Attributes and values align
by index. Absent values are null. pageRating is Ahrefs' 0–100 Page Rating;
traffic is the crawl's estimated monthly organic traffic, lengths are characters.
Only Simple fields in the Pages table are currently supported.

auth import:
  --browser-profile P   Chrome profile name or directory (default Default)
  --cookies-file FILE   Explicit cookie JSON via SweetCookie instead of Chrome
                        Imports only; no network request

Options (after any command):
  --session-file PATH   Override the saved owner-only Ahrefs cookie jar
  --timeout DURATION    Whole-command deadline, e.g. 60s (default 60s)
  --json                Accepted; output is always JSON
  -h, --help            Show this help without accessing cookies or the network

Sign in to app.ahrefs.com in the selected Chrome profile, then run auth import.
Only import accesses Chrome/SweetCookie and may require OS Keychain access.
It saves Ahrefs session/protection cookies in the OS config directory under
ahrefs/session.json with owner-only permissions. Workflow commands read this
jar directly without Chrome or Keychain access. auth status reads the jar
locally; session_available means a local unexpired session cookie exists,
not that Ahrefs has validated it. Cookie values are never printed.
Run auth import again after signing in or when saved cookies expire.

All network commands are read-only web queries, even though Ahrefs uses POST.
No API key, paid export, crawl start, settings change or browser automation is
used at runtime. HTTP errors, redirects, login/protection pages and rate limits
stop the command without retries. Honor Retry-After and resolve the condition
before rerunning. Errors go to stderr; failed commands emit no result JSON.

Examples:
  go run ./ahrefs issues 123 --crawl 31-10-2026T161144
  go run ./ahrefs issue 123 ISSUE_ID
  go run ./ahrefs pages 123 --crawl 31-10-2026T161144 --issue ISSUE_ID --all
  go run ./ahrefs pages 'https://app.ahrefs.com/site-audit/123/data-explorer?current=31-10-2026T161144&issueId=ISSUE_ID&filterId=FILTER_ID&columns=url,httpCode'
`
