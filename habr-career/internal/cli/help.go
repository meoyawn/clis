package cli

const help = `habr-career — Habr Career through a SweetCookie-imported Chrome session; JSON

Usage: habr-career COMMAND [OPTIONS]

  auth import [--browser-profile Default] [--cookies-file FILE]
      Import Habr Career session/protection cookies through SweetCookie.
      Only this command accesses Chrome/Keychain. No network request.
  auth status
      Inspect the saved jar locally. session_available means an unexpired local
      session cookie exists, not that the website has validated authentication.
  resumes list
      List the account's native resume with its stable alias and profile URL.
      Habr has one shared native profile, not HH's separate resume variants.
  profile get
      Read the signed-in account identity, email and profile settings. GET only.
  resume get ALIAS
  resume view ALIAS
      Read the exact native profile, verify ownership/identity and include its
      actual rendered text, contacts, experience and resume fields. GET only.
      No default resume, numeric list index or title-based selection.
  vacancy get ID
  vacancy response-options ID
      Read the live role and native response preflight: employer pay, location,
      requirements, response, fields, restrictions and signed-in alias. GET only.
      predicted_salary is Habr's estimate, never an employer-confirmed budget.
      Never execute the website's quick-apply POST during inspection.
  application get VACANCY --resume ALIAS
  application letter VACANCY --resume ALIAS
      Read this exact vacancy's native response ID, author and current letter.
      An absent response is reported as submitted:false. GET only; never sends
      messages, marks them read or edits a response.
  application prepare VACANCY --resume ALIAS --text-file FILE [--output FILE]
      Build an exact local review package using GET only. Bind destination,
      sender, native profile content and complete letter to review_sha256.
      The salary field is optional and also changes the shared profile; this
      CLI leaves it untouched. External application routes and unknown forms stop.
  application submit --draft FILE [--send --approved SHA256] [--output FILE]
      Without --send, display the saved package locally without authentication.
      --send requires prior user authorization and the unchanged package checksum;
      a checksum is not proof of authorization. Follow the workspace's review rules.
      Fresh GET preflight rejects changed role/profile/form, duplicate responses,
      restrictions and account switches. Habr creates a response with one POST,
      then saves the cover letter with one PATCH. Verify the response/author/ID
      after creation and independently read back the exact saved letter.
      A later failure can leave an application without its letter: inspect
      application get/letter; NEVER resubmit blindly. Never send live test traffic.
  har inspect FILE
      Offline method/path/status and request/query field names from a saved HAR.
      Never print cookie/header/body values or replay recorded requests.

Common options (after COMMAND, including after its ID):
  --session-file FILE   Override the saved owner-only cookie jar
  --output FILE         Save successful JSON atomically with mode 0600
  --timeout DURATION    Whole-command deadline (default 90s)
  --help, -h            Help without Chrome, cookies or network access

Vacancy accepts a numeric ID or https://career.habr.com/vacancies/ID.
Resume accepts the stable alias or https://career.habr.com/ALIAS.
Sign in to career.habr.com in Chrome, then auth import. The saved jar is under
the OS config directory at habr-career/session.json, owner-only. Workflow commands
read it directly; they never reopen Chrome/Keychain or automatically import.
Run auth import again after signing in or when cookies expire. Cookie values,
CSRF tokens and raw private HTML are never printed or saved in review packages.

All account network commands take one exclusive lock; run one at a time.
No automatic retries. Stop on redirects, login/protection/CAPTCHA/checkpoints,
429 (honor Retry-After), restrictions or unsupported contracts. No CAPTCHA solving.
Only application submit with both sending flags writes. No profile edits, uploads,
salary changes, paid services or messages are performed by the commands above.
stdout contains successful JSON only; bounded, redacted HTTP errors go to stderr.

Do tasks through go run . in /Users/meoyawn/Developer/clis/habr-career/.
If support is missing/broken, change this CLI, test it and resume through the CLI.
Inspect saved captures first; signed-in Chrome is only for discovering missing
HTTP contracts, never a browser workaround. Keep captures and private exports
outside Git. Tool development does not authorize applications or account changes.
`
