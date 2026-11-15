# hh

- All HTTP operations use `service.Client.requestAt`. `BrowserClient` owns the
  shared Chrome transport, scoped cookies, timeouts and redirect policy. Native
  service adapters supply payloads/context; do not add separate HTTP send paths.
- Derive the default User-Agent from installed Chrome. Use `--headers-file` for
  exact headers from the selected session; never invent client hints, browser
  fingerprints, CAPTCHA proof or authentication values.
- Native wire-contract changes need anonymized Chrome request/response captures
  and strict local replay checks. Compare the complete multipart bytes and
  application-header map, rejecting unexpected fields or headers. Document every
  volatile substitution and transport-level limit; do not claim TLS equivalence
  from HTTP mock tests.
- Raw captures, cookies, CSRF/CAPTCHA values, applicant text, contacts and account
  IDs stay outside Git. Fixtures use synthetic identities, text, locale and secret
  sentinels. Keep the fixture privacy gate and verify new fixtures are not ignored.
- Never submit an application or send a message as a live test. CAPTCHA stops the
  CLI; a human resolves it in the existing Chrome session before any continuation.
