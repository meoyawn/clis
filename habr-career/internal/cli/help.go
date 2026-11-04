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
  resume experiences ALIAS
      List the account's native numeric experience IDs and exact edit URLs.
  resume experience ALIAS --experience ID
      Read one exact native employment form: company, position, HTML description
      and current metadata. GET only; retains repeated skill/select form fields.
  resume update ALIAS --experience ID --description-file FILE [--apply] [--backup-dir DIR]
      Preview or replace only that existing employment description (native HTML).
      --apply requires the user's instruction, saves a private backup, posts the
      fresh native form once, preserves metadata, and verifies saved fields,
      employer print text and unchanged profile/other employment passages.
      Does not change contact details, salary, skills, dates, title or visibility.
  resume verify ALIAS --experience ID --description-file FILE
      GET-only description/form and employer print verification after an uncertain
      update. Never replay a write. Use the exact description file from the update.
  vacancy get ID
  vacancy response-options ID
      Read the live role and native response preflight: employer pay, location,
      requirements, response, fields, restrictions and signed-in alias. GET only.
      predicted_salary is Habr's estimate, never an employer-confirmed budget.
      Never execute the website's quick-apply POST during inspection.
  applications list
      Read all native active-response pages (excluding deleted). Verify account,
      role URLs and the total count; preserve native status and application date.
      Follow only the site's own pagination links. GET only; no messages opened.
  application get VACANCY --resume ALIAS
  application letter VACANCY --resume ALIAS
      Read this exact vacancy's native response ID, author and current letter.
      An absent response is reported as submitted:false. GET only; never sends
      messages, marks them read or edits a response.
  application verify --draft FILE
      Read-only recovery/verification against the reviewed package. Bind the
      native response to its exact vacancy/author and letter. Habr removes final
      line breaks; only terminal CR/LF differences are accepted and reported with
      terminal_newlines_normalized plus the exact submitted_letter. Other text
      differences stop. Never recreate or edit an application during verification.
  application update VACANCY --resume ALIAS --text-file FILE [--apply] [--backup-dir DIR]
      Preview or replace the letter of an existing editable response. Bind its
      vacancy, author and response ID, back up the old letter, PATCH body only,
      then read back the same ID and exact text (terminal CR/LF may be removed).
      Never create another response, change salary or send a separate message.
      --apply requires user authorization. After uncertainty use application letter.
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
      after creation and independently read back the letter (see verify above).
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
Only application submit with both sending flags and update commands with --apply
write. Profile edits are limited to one employment description; no uploads,
salary/contact changes, paid services or separate messages are performed.
Backups default to $XDG_STATE_HOME/habr-career/backups or ~/.local/state/habr-career/backups.
stdout contains successful JSON only; bounded, redacted HTTP errors go to stderr.

Do tasks through go run . in /Users/meoyawn/Developer/clis/habr-career/.
If support is missing/broken, change this CLI, test it and resume through the CLI.
Inspect saved captures first; signed-in Chrome is only for discovering missing
HTTP contracts, never a browser workaround. Keep captures and private exports
outside Git. Tool development does not authorize applications or account changes.
`
