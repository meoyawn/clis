# twitter

A Go CLI for X using an existing Chrome session imported with
[SweetCookie](https://github.com/steipete/sweetcookie). Run from this directory:

```sh
go run . --help
go run . auth import --profile Default
go run . auth status
go run . me
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
reply target, native media IDs and available views/engagement counters. Quoted
posts do not substitute for the focal post. `posts list` reads one page of up to
20 own original posts, retaining counters and a fetch timestamp. It does not
exhaust historical posts or replies, infer unseen views as zero, or attribute
job-search outcomes to a post. Profile output excludes private contact fields.

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
