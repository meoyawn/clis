# linkedin

A Go CLI that returns LinkedIn people searches, company people, profiles, mutual connections, and messages as JSON using your browser's
signed-in session. Cookie import uses [SweetCookie](https://github.com/steipete/sweetcookie/).

```sh
go build -o linkedin .
./linkedin auth login --browser chrome
./linkedin companies people example-company
```

To install `linkedin` on your PATH:

```sh
go install .
```

Requires Go 1.27.2 or newer. Examples use placeholder company slugs and IDs;
replace them with the company you want to look up.

```sh
linkedin --help
linkedin companies people --help
linkedin people search --help
linkedin people profile --help
linkedin people mutuals --help
linkedin auth login --help
```

Every command supports `-h` and `--help`. Use `linkedin help <command>` for
the same help, for example `linkedin help companies people`.

## Authentication

Sign in to linkedin.com in your browser, then import its cookies:

```sh
linkedin auth login                         # first usable browser session
linkedin auth login --browser chrome --profile 'Your Chrome'
linkedin auth login --browser arc
linkedin auth login --cookies-file /path/to/cookies.json
linkedin auth status
linkedin auth logout
```

The JSON import accepts SweetCookie's cookie array or `{ "cookies": [...] }`
format. It must contain `li_at` and `JSESSIONID` for `.linkedin.com` from the
same signed-in browser profile. Browser import may prompt for Keychain/keyring
access. Only those two cookies are saved, in `cookies.json` under the OS user
configuration directory's `linkedin` folder (on macOS,
`~/Library/Application Support/linkedin`). The directory is mode `0700`, and
the file is mode `0600`. Set `LINKEDIN_CONFIG_DIR` to choose another directory.

Auth commands work locally without HTTP calls. `auth status` checks whether the
saved cookies exist and have expired; it does not validate them with LinkedIn.
Cookie values are never included in command output. `logout` removes the local
copy; it does not sign your browser out. Re-run `auth login` after refreshing
your browser session if LinkedIn rejects the cookies.

## Company people

Company accepts the URL slug, numeric ID,
company URL (including `/people/`), or organization URN:

```sh
linkedin companies people example-company
linkedin companies people https://www.linkedin.com/company/example-company/
linkedin companies people 123456789
linkedin companies people example-company --limit 24
linkedin companies people example-company --start 49
linkedin companies people example-company --all
```

The default returns up to 49 people. `--limit N` requests up to N;
`--all` fetches all results LinkedIn makes available and overrides `--limit`.
`--start N` resumes at a result offset. Flags can precede or follow the company.

Output includes `company`, `people`, `total`, `returned`, `start`, `has_more`,
and `next_start`. Each person has `name`, `headline`, `location`, `url`, and
available profile/member URNs and connection degree. Results retain LinkedIn's
search order; profile URLs have tracking parameters removed. `next_start` is
`null` when there are no more pages. `total` is LinkedIn's reported search count,
which can change or be restricted by your account.

Successful commands emit JSON to stdout. Help is plain text. Diagnostics go to
stderr; failures exit nonzero without emitting a partial result.

## Keyword people search

Run one query or a batch of name/affiliation variants:

```sh
go run . people search 'Trung Kien Ngo Oulu'
go run . people search 'Trung Kien Ngo Oulu' 'Ngo Trung Kien Palermo' --limit 5
go run . people search 'Example Person Example Company' --save-response private/search.json
```

Quote each complete query separately. Queries run sequentially with the usual
request spacing; they are not combined into a single Boolean query. Use relevant
keywords directly, without a web-search-only `LinkedIn` suffix or `site:` operator.

Output contains `searches` and `fetched_at`. Each search preserves its `query`,
`search_url`, `people`, `returned`, and `has_more`. The first page only is read;
`--limit` accepts 1–10 (default 10) per query. `has_more` also includes results
omitted by that limit; no total or complete-coverage claim is inferred.

Each query makes one authenticated HTML GET to the captured people-search route.
The parser verifies the search box's keyword state, including the initial
document's JSON hydration records, without executing scripts. It does not replay
typeahead, search-history, analytics, or profile-prefetch requests. Failures stop
the batch without retries, later queries, or partial JSON. Zero matches require
the explicit visible "No results found" state; unrecognized pages fail.

Search results are discovery candidates, not confirmed identities or mutuals.
Read the strongest candidate with `people profile`, corroborate the affiliation,
then use `people mutuals` for verified introduction paths. A zero-person company
listing does not establish that no personal profile or warm path exists.
`--save-response` writes raw documents to a new private 0600 file, including
available documents on failure; never commit those captures.

## Profiles and referral introductions

```sh
linkedin people profile example-person
linkedin people profile https://www.linkedin.com/in/example-person/
linkedin people profile example-person --summary
linkedin people profile example-person --save-response private/profile.json
linkedin people mutuals example-person --limit 3
linkedin people mutuals urn:li:fsd_profile:ACoExample
```

`people profile` returns the name, headline, about text, canonical profile URL,
available location, profile/member URNs, and `sections` containing readable text
blocks and links with a `source_url` for each section. It resolves the profile
with one API call, then reads the profile overview and full experience document
concurrently with `errgroup`: three GET requests total, not one call per role.
The experience page retains position groups, dates, locations, descriptions,
skills attached to positions, and linked work. `fetched_at` and `coverage` make
the observation's scope explicit. Lazy overview sections and further pagination
are not inferred to be empty. No browser scripts or analytics run.

`--summary` preserves the original one-request identity/summary response. Internal
messaging and mutual lookups keep using that lightweight API. Neither output
exports private contact fields. A profile document may contain multiple degree
UI alternatives; the exporter omits them rather than claiming a relationship.
Use `people mutuals` to establish an exact-target introduction path.

`--save-response PATH` saves the raw API and document responses for inspecting a
changed layout. The capture uses mode 0600, never overwrites an existing file,
and can contain private profile data. It is separate from successful stdout.

`people mutuals` resolves the target profile, loads its document, and follows
only a link explicitly labelled as mutual connections by that profile, for its
exact identifier and first-degree network. If LinkedIn does not expose that
action, it fails without searching or returning generic contacts. Successful output includes
`verified: true`, the canonical target profile, the observed `search_url`, and
the source label in `profile_evidence`; older cached output without verification
is unverified. Verification establishes the LinkedIn introduction path, not
willingness to refer or the strength of the relationship.
It reads up to 10 contacts from the first page and reports `has_more` without
fetching another page. `--limit` accepts 1–10. All inputs need three sequential
requests on success, including profile URNs. It parses result cards using
semantic attributes, ignoring sidebar/related-person links and scripts.
An unrecognized response or non-first-degree result fails explicitly.
No scripts, analytics, messages, or connection requests are executed.
Results are potential introduction paths; willingness to refer remains unconfirmed.

To inspect a changed response, pass `--save-response private/mutuals.html`
after creating the ignored `private/` directory. The new capture file is written
with mode 0600 and is never overwritten. It can contain private profile data.

## Connection requests

```sh
go run . people connect https://www.linkedin.com/in/example-person/
go run . people connect example-person --send
```

Run from this module. The default is a read-only preview with the signed-in
sender, resolved recipient, empty `note`, `can_connect`, `pending`,
`profile_evidence`, `sent` and `checked_at`. Review the exact sender and recipient
with the human before using `--send`. This command sends without a note only.

The profile must render a Connect control bound to that recipient's member ID,
name and invitation URL. Serialized UI alternatives, other people's controls,
disabled actions and ambiguous states do not authorize a send. A target-specific
Pending control returns a no-op even with `--send`; the command never withdraws,
accepts or resends an existing invitation. Unsupported states, including profiles
already connected without a supported control, fail without a POST.

The endpoint and no-note payload were captured from Chrome on 26 October 2026
using request-stage interception; the inspection request was aborted before
transmission. An approved send makes one `verifyQuotaAndCreateV2` POST and one
profile readback. `sent:true` requires the exact target's Pending control; HTTP
success alone is insufficient and Pending does not mean the person accepted.
There are no automatic retries. After an uncertain outcome, inspect with the
read-only command before deciding whether to retry. Tests use synthetic profiles
and mocked requests, never live invitations.

Preview makes four sequential GETs. Sending a new invitation adds one POST and
one GET. Authentication failures, restrictions and rate limits stop the command.

## Direct messages and free InMail

```sh
linkedin messages read example-person
linkedin messages send example-person --text-file /path/to/message.txt
linkedin messages send example-person --text-file /path/to/message.txt --send
```

`messages send` previews by default. Show the human the exact recipient and final
text, including links, and wait for explicit approval before adding `--send`.
Use UTF-8 plain text with ordinary URLs: LinkedIn does not interpret Markdown.
File contents, including newlines, are sent unchanged.

For a new conversation, preview and send both check the exact recipient's
compose options. Open Profile recipients with a confirmed "Free message"
context use the InMail payload automatically; preview returns `free_inmail:true`.
No subject is added. Paid, blocked, unknown, or unsupported compose modes stop
without sending or spending credits.

Both commands identify the signed-in mailbox and resolve the recipient, then
look up their direct conversation. If present, they verify its two participants
before reading one page of recent messages. Reading sends no read receipts.
No historical pagination is performed. Sending first checks that page for an
identical outgoing message and returns `already_present:true` without sending
it again. This does not search older messages or prevent concurrent sends.

Read output preserves `renderContent`, including InMail's `hostUrnData.type`.
LinkedIn's message presenter suppresses the normal sent indicator when the
message subtype is `INMAIL`, including free Open Profile InMail. Becoming
connections does not reclassify an existing InMail message. A stored message,
`deliveredAt`, or `sent:true` receipt confirms server acceptance, not that the
recipient has received or read it.

An actual send makes a single POST using the observed `createMessage` contract.
Only a matching server receipt is reported as `sent:true`. No send is retried
automatically. After an uncertain outcome, read the conversation and inspect
the returned text and origin token before deciding whether to send again.
Test changes with local fixtures or mocks; never send live test messages.

## HTTP budget

Reading messages uses three GET requests without an existing conversation and
four with one. A new-message preview/send adds one compose-options GET and,
for free InMail, one compose-context GET. Actual sending adds one POST.

The company people API accepts the company ID directly, so no general search or HTML
page load is needed.

- Slug or slug URL: one lookup, then one request per page.
- Numeric ID, ID URL, or organization URN: one request per page.
- Pages request up to 49 people, with smaller counts for the final limit.
- Requests within one command start at least two seconds apart, except for the
  two independent profile-document reads explicitly run concurrently. Run separate
  commands sequentially and wait at least two seconds between them; independent
  processes do not share a rate limiter. These are local operating defaults,
  not published LinkedIn safe limits.
- No browser navigation, profile-by-profile lookups, analytics, or eager
  pagination. No automatic retries on failures or rate limits.

Authentication redirects, checkpoints, and rate limits stop the command; it
does not attempt to work around them. Re-import cookies after signing in again.
A later page that unexpectedly reports zero total results fails explicitly
instead of claiming an incomplete export is complete. These limits reduce
unnecessary traffic; unofficial API access can still trigger LinkedIn checks.

This uses LinkedIn's browser API and observed persisted query IDs.
If LinkedIn changes that API, the CLI reports an error. HAR files, credentials,
and the compiled executable are excluded from version control. Test fixtures
retain the captured response structure with synthetic people and no cookies.

HTTP and transport failures include detailed diagnostics on stderr: method,
URL, timing, selected request/response headers, request JSON structure, and up
to 16 KiB of the error response (including service error codes and messages).
Cookie/auth headers are omitted; credentials, message text, and subjects are
redacted. stdout remains reserved for successful JSON results. Diagnostics can
still contain recipient identifiers and server error details, so review them
before sharing.

```sh
go test ./...
moon run linkedin:lint
```

## Privacy

Browser cookies and exported people data are private. Keep them outside this
repository, and remove cookies, authorization headers, profile identifiers,
and personal data before sharing logs, network captures, or bug reports.
The fixtures in `testdata/` use synthetic people and placeholder company IDs.

## License

Copyright (C) 2026 meoyawn. Licensed under the GNU General Public License,
version 3 only ([GPL-3.0-only](LICENSE)).
