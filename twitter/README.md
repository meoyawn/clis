# twitter

A Go CLI for X using an existing Chrome session imported with
[SweetCookie](https://github.com/steipete/sweetcookie). Run from this directory:

```sh
go run . --help
go run . auth import --profile Default
go run . auth status
go run . me
go run . api status
go run . posts list
go run . posts read https://x.com/example/status/123456789
```

Requires Go 1.27.2. `main.go` delegates to `internal/cli`; account and HTTP logic
live in `internal/service`, with post references and drafts in `internal/domain`.

## Session and reads

Import/status are local-only. Only `auth_token`, `ct0` and `twid` from the same
Chrome source are saved, in the OS user configuration directory's `twitter`
folder. The directory is 0700 and session file 0600. `TWITTER_CONFIG_DIR` overrides
the directory. Keychain access may be requested by SweetCookie. Status does not
make a network request or prove that X still accepts the session.

Network commands first read X's signed-in `/home` bootstrap, fetch its public
main bundle, discover persisted query IDs and the public client authorization,
then verify the account against the imported cookie identity. Client authorization
is transient, never committed or printed. Cookies are restricted to `x.com` and
the observed `upload.x.com` endpoint; they never accompany public bundle requests.
Scripts, telemetry and browser actions are not executed.

`posts read` returns the exact focal post's author, expanded URLs, creation time,
reply target, native media IDs, direct image URLs (`image_urls`, from photo
`media_url_https` fields) and available views/engagement counters. These public
CDN URLs can be fetched without session credentials. Video/GIF thumbnails are
excluded; the CLI does not download images. Quoted
posts do not substitute for the focal post. `posts list` reads one page of up to
20 own original posts, retaining counters and a fetch timestamp. It does not
exhaust historical posts or replies, infer unseen views as zero, or attribute
job-search outcomes to a post. Profile output excludes private contact fields.

`posts read` also returns `parents` in order from the oldest available ancestor
to the immediate parent, and `replies` containing visible direct replies by any
author. It makes one conversation request and follows parent links within that
response; it never fetches missing ancestors or paginates replies. A chain that
reaches the root has `parents_complete: true`. Otherwise, `parents_complete` is
false and `missing_parent_id` identifies the first unavailable ancestor. Empty
parent/reply lists are arrays. The focal post's numeric `replies` counter remains
separate from the top-level reply list; reply history is not exhausted.

`posts replies POST_URL` checks direct own replies in the conversation and the
first own-replies timeline page. This is a bounded read, not an exhaustive history.
`posts read` and `posts replies` accept `--save-response NEW.json` before the ID to
save the raw conversation response privately for debugging.

## Preview and publish

```sh
go run . posts create --text-file /path/to/post.txt --image /path/to/illustration.webp
go run . posts create --text-file /path/to/post.txt --image /path/to/illustration.webp --publish --expect-author example --expect-sha256 PACKAGE_SHA256
go run . posts create --text-file /path/to/link.txt --reply-to https://x.com/example/status/123456789
```

Default create is read-only and uploads nothing. Review the exact author, text,
image path/hash, and reply target with the human. Only publish an approved package
using its `package_sha256` and author handle. The hash covers author identity,
text, image bytes and reply target, so edits require another preview and review.
Replies are restricted to the signed-in account's own posts.

One static PNG/JPEG/WebP image up to 5 MiB is supported. Publishing uploads the
reviewed bytes using INIT, APPEND and FINALIZE before CreateTweet. Video, animated
GIFs, polls, scheduling, bulk posts, long posts and replies to other people are
unsupported. Text is never truncated. The CLI uses a conservative bound based on
[twitter-text v3](https://github.com/twitter/twitter-text/blob/master/config/v3.json):
Unicode weights apply, but URLs before shortening and complex emoji sequences
may be overcounted. This can reject some otherwise valid 280-character posts.

A private 0600 attempt is saved before the first mutation. There is one create
attempt, followed by independent focal-post readback. Only matching author, text,
media IDs and reply target yield `published:true` and `verified:true`. Confirmed
duplicates are no-ops; unconfirmed records block another attempt even after a
timeout or process restart. Inspect your profile and the record before manually
reconciling it. Do not delete a record and blindly retry.

The raw create response is reserved in a separate 0600 receipt file before
transmission and retained even for HTTP/API errors or an empty create result.
An empty result is unconfirmed, never a successful publication or permission to
resend. Read the exact post and own replies before reconciling a saved attempt.

Writes derive fresh `x-client-transaction-id` metadata from the current bootstrap
verification key, animation frames and public `ondemand.s` bundle. Values are
bound to the HTTP method, pathname and time; they are never replayed or printed.
`api status` verifies that these materials can be read and shows public query
IDs. Missing/changed materials stop before uploading or publishing.
The pure Go animation/signing implementation derives from
[George Santiago's MIT implementation](https://github.com/ichioda/x-client-transaction-id),
with attribution in `internal/service/transaction/LICENSE`. It performs no HTTP,
executes no website scripts, and uses the owning service's explicit HTTP client.

The contracts were inspected in signed-in Chrome on 27 October 2026. Publish
and reply requests were intercepted at request stage and aborted before
transmission. Public article artwork was prepared as an unpublished upload.
Live account identity, post reads and counters were verified through the CLI;
publication is covered with synthetic HTTP tests, never live test posts.

## Failures and validation

HTTP 429, authentication redirects/errors, API errors and restrictions stop the
command with no automatic retry. `Retry-After` is retained in diagnostics. Run
account commands sequentially. If X changes its bootstrap or GraphQL contract,
inspect the exact signed-in Chrome traffic and update the CLI; do not guess IDs,
switch accounts, repeatedly import cookies or use a mirror to evade a restriction.

```sh
moon run twitter:check
```

Tests exercise private attempt persistence before upload, exact uploaded bytes,
receipt/readback verification, duplicate suppression, uncertain outcomes and
input validation. Fixtures contain synthetic identities only. Keep sessions,
raw bootstrap documents, real account exports and captures outside Git.

Create receipts can omit duplicate legacy identifiers. The CLI uses the returned
post ID and author identity to read the exact post independently, then verifies
the approved text, uploaded media IDs and reply target. A receipt parsing failure
does not establish that publication failed; inspect `posts list` and `posts read`
before reconciling the private attempt. Never repeat a publish merely to repair
its local receipt.
