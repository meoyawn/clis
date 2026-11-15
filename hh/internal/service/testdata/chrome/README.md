# Recorded Chrome submission contract

Captured from Chrome 157 on 15 November 2026. `submission-initial.json` records
the ordinary form POST and CAPTCHA 403; `submission-after-captcha.json` records
the form after a human completed that challenge and the successful receipt.
The latter's CAPTCHA fields are synthetic codec inputs, not a challenge solver
or permission to retry a live application.

The multipart field set, order, field headers, CRLF rules, UTF-8 and final boundary
come from the recordings. CSRF/CAPTCHA values, résumé/vacancy/topic/chat IDs,
letter and language preference have explicit synthetic replacements. The success
response retains only receipt fields; account/profile/address data was removed.
Raw captures are outside this repository. No recorded cookie values, contacts,
real applicant text, fingerprint values, IP addresses or telemetry IDs belong here.

The local replay server compares every multipart byte after substituting only the
random boundary. It compares the complete application-header map after replacing
the local test origin. Cookies come from a minimal synthetic session jar, and the
CSRF header and form field must both agree with that jar. Unknown extra fields or
application headers fail the comparison. Boundary prefix/alphabet/length and
fixed Content-Length are checked too.

The test checks Go's gzip negotiation separately. HTTP/2 pseudoheaders, TLS/HTTP2
fingerprints, browser-only tracing/telemetry and real tracking-cookie inventory
are outside this HTTP application-contract test. It does not prove byte-for-byte
TLS impersonation or that a server will waive CAPTCHA. Captured safe browser
metadata is passed through `--headers-file`; default mode only derives the
installed Chrome User-Agent and does not fabricate client hints.

Boundary generation follows Chromium's implementation:
https://chromium.googlesource.com/chromium/src/+/refs/heads/main/third_party/blink/renderer/platform/network/form_data_encoder.cc
