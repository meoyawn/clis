package cli

import (
	"fmt"
	"io"
	"strings"
)

const rootHelp = `LinkedIn people, profiles, mutual connections, and messages, as JSON.

Usage:
  linkedin <command> [flags]

Commands:
  companies people <company>  List people at a company
  people search <query>...    Search people with one or more keyword queries
  people profile <person>     Read profile details and experience
  people mutuals <person>     Find shared first-degree connections
  people connect <person>     Preview or send a connection request without a note
  experience update <person> Preview or update your experience description
  messages read <person>      Read recent messages in a direct conversation
  messages send <person>      Preview or send approved plain text
  posts create                Preview or publish an approved personal feed post
  posts read <post>            Read one exact feed post
  posts edit <post>            Preview or apply a text edit to your own post
  posts comment <post>         Preview or publish a comment on your own post
  posts comments <post>        Read the rendered comments on an exact post
  articles read-draft <draft>  Read one exact personal article draft
  articles delete-draft <id>   Preview or delete an exact unpublished draft
  api capture                 Save a private read-only contract capture
  analytics summary           Read your profile and content analytics
  analytics compare <a> <b>    Compare two local analytics snapshots
  auth login                  Import browser cookies with SweetCookie
  auth status                 Check saved cookies locally
  auth logout                 Remove saved cookies
  help [command]              Show help for a command

Quick start:
  linkedin auth login --browser chrome
  linkedin companies people example-company
  linkedin companies people example-company --all

Options:
  -h, --help                  Show help

People:
  Accepts a company slug, numeric ID, LinkedIn company URL, or organization URN.
  Returns up to %d people by default. Use --all to fetch every available page.
  Results follow LinkedIn's search order. Numeric IDs skip the company lookup.

Output:
  Commands print JSON to stdout; errors go to stderr and exit nonzero.

Environment:
  LINKEDIN_CONFIG_DIR         Override the local cookie storage directory

Run 'linkedin <command> --help' for command options and examples.
`

const companiesHelp = `Look up people at LinkedIn companies.

Usage:
  linkedin companies <command>

Commands:
  people <company>           List people at a company as JSON

Examples:
  linkedin companies people example-company
  linkedin companies people https://www.linkedin.com/company/example-company/

Run 'linkedin companies people --help' for options.
`

const peopleHelp = `List people at a LinkedIn company as JSON.

Usage:
  linkedin companies people <company> [flags]

Arguments:
  <company>                  Company slug, numeric ID, LinkedIn company URL,
                             or organization URN

Options:
      --limit N              Maximum people to return (default: %d)
      --start N              Start at this result offset (default: 0)
      --all                  Fetch every available page; overrides --limit
  -h, --help                 Show help

Examples:
  linkedin companies people example-company
  linkedin companies people example-company --all
  linkedin companies people example-company --limit 10
  linkedin companies people 123456789

Flags may precede or follow the company. Sign in with 'linkedin auth login'.
Each people request fetches up to %d results. Slugs need one extra lookup;
numeric IDs skip it. Requests are not retried automatically.

Output includes company, people, total, returned, start, has_more, and next_start.
Use next_start with --start to continue. Errors exit nonzero without partial JSON.
`

const profilesHelp = `Read LinkedIn profiles and find referral introduction paths.

Usage:
  linkedin people <command> <person> [flags]

Commands:
  search <query>...           Search people by name, company, school, or keywords
  profile <person>            Read identity, about text, and sourced experience
  mutuals <person>            Read up to 10 mutual connections from the first page
  connect <person>            Preview or send an approved connection request

Accepts a profile slug, https://www.linkedin.com/in/<person>/ URL, or a
urn:li:fsd_profile identifier from companies people output.

Run 'linkedin people <command> --help' for options.
`

const peopleSearchHelp = `Search LinkedIn people by keywords, as JSON.

Usage:
  linkedin people search <query> [<query>...] [flags]

Quote each complete query separately. Multiple queries run sequentially.
Use names and relevant company/school keywords; do not add a web-search-only
"LinkedIn" suffix or site:linkedin.com operator.

Options:
      --limit N             Maximum people per query from its first page (1–10, default: 10)
      --save-response PATH  Save raw documents to a new private JSON file (0600)
  -h, --help                 Show help

Examples:
  linkedin people search 'Trung Kien Ngo Oulu'
  linkedin people search 'Trung Kien Ngo Oulu' 'Ngo Trung Kien Palermo'
  linkedin people search 'Example Person Example Company' --limit 3

Output has searches (query, search_url, people, returned, has_more) and fetched_at.
One authenticated document GET per query, with the usual request spacing.
The first page only is read; has_more also covers results omitted by --limit.
No total is inferred. Search results are candidates, not verified identities or
mutual connections: use people profile, then people mutuals for a warm path.
Failures stop the batch without retries, subsequent queries, or partial JSON.
Zero matches require an explicit "No results found" state; unknown pages fail.
Captures may contain private data and never overwrite an existing file.
`

const profileHelp = `Read a LinkedIn profile as JSON.

Usage:
  linkedin people profile <person> [--summary] [--save-response PATH]

Arguments:
  <person>                   Profile slug, /in/ URL, or urn:li:fsd_profile identifier

Options:
      --summary             Only fetch identity and summary (one API request)
      --save-response PATH  Save raw responses to a new private JSON file (0600)
  -h, --help                 Show help

Examples:
  linkedin people profile example-person
  linkedin people profile https://www.linkedin.com/in/example-person/
  linkedin people profile example-person --summary

Default output adds sourced profile sections, full experience-page text,
descriptions, dates, links, fetched_at, and explicit coverage limits. Three GETs:
one identity API call, then two independent document reads using errgroup.
The two document reads run concurrently; other CLI commands remain sequential.
No browser scripts, contact-info overlay, messages, or analytics are executed.
Absent lazy sections or further pagination are not treated as empty.
Use people mutuals for verified introduction paths, not degree UI alternatives.
--summary keeps the lightweight identity/summary output used by internal lookups.
Raw captures can contain private profile data and never overwrite an existing file.
`

const mutualsHelp = `Find shared first-degree connections with a LinkedIn person.

Usage:
  linkedin people mutuals <person> [--limit N]

Arguments:
  <person>                   Profile slug, /in/ URL, or urn:li:fsd_profile identifier

Options:
      --limit N              Maximum contacts from the first page (1–10, default: 10)
      --save-response PATH   Save fetched HTML to a new file (0600); may contain private data
  -h, --help                 Show help

Examples:
  linkedin people mutuals example-person
  linkedin people mutuals https://www.linkedin.com/in/example-person/ --limit 3
  linkedin people mutuals urn:li:fsd_profile:ACoExample

Resolve the target and require its profile to expose an exact mutual-search link
labelled as mutual connections before reading the first page (three sequential
requests on success). Missing confirmation fails without returning generic
contacts. Successful output includes verified:true and profile_evidence with
the source label; older cached results are unverified. has_more reports additional
results without fetching them. No --all or automatic pagination. No scripts,
analytics, messages, or connection requests are executed. Names are potential
introduction paths; a result does not establish willingness to refer you.

Output includes profile, search_url, profile_evidence, people, returned,
has_more, and verified.
`

const connectHelp = `Preview or send one LinkedIn connection request without a note.

Usage:
  linkedin people connect <person> [--send]

Accepts a profile slug, /in/ URL, or urn:li:fsd_profile identifier.

Options:
      --send                Send only after approval of this sender and recipient
  -h, --help                Show help

Examples:
  linkedin people connect https://www.linkedin.com/in/example-person/
  linkedin people connect example-person --send

Default is read-only: identify sender and recipient, then verify the exact
target's Connect or Pending control, including the active More-menu state.
Output includes sender, recipient,
note (always empty), can_connect, pending, profile_evidence, sent and checked_at.
Pending invitations are a no-op, including with --send. Already connected,
restricted, unsupported or ambiguous profiles never trigger a POST.

With --send, make one observed verifyQuotaAndCreateV2 POST, then read the profile
once. sent:true requires a confirmed Pending control; it does not mean accepted.
No automatic retries, withdrawals, invitation acceptance, notes or bulk sends.
After an uncertain outcome, inspect with people connect before deciding to retry.
Four sequential GETs for preview; a new send adds one POST and one GET.
`

const authHelp = `Manage the browser cookies used to sign in to LinkedIn.

Usage:
  linkedin auth <command> [flags]

Commands:
  login                      Import cookies with SweetCookie
  status                     Check saved cookies locally
  logout                     Remove the local cookie copy

Examples:
  linkedin auth login --browser chrome
  linkedin auth login --browser arc
  linkedin auth status

Cookies are stored locally with restricted file permissions. Auth commands
make no HTTP requests. Set LINKEDIN_CONFIG_DIR to choose the storage directory.

Run 'linkedin auth <command> --help' for options.
`

const messagesHelp = `Read direct conversations and send approved plain text.

Usage:
  linkedin messages read <person>
  linkedin messages send <person> --text-file PATH [--send]

The person is a LinkedIn profile slug, /in/ URL, or fsd_profile URN.
Run each command sequentially. Authentication failures and rate limits stop
the command without retries. No read receipts are sent by the read command.

Run 'linkedin messages read --help' or 'linkedin messages send --help'.
`

const messagesReadHelp = `Read recent messages with one exact LinkedIn recipient.

Usage:
  linkedin messages read <person>

Resolves the signed-in mailbox and recipient, verifies a direct conversation,
and reads one page of recent messages. No pagination or read receipts.
An empty conversation list is returned as messages:[]; unexpected or ambiguous
responses fail. Returned text and identifiers may contain private data.
Render content is preserved, including hostUrnData that identifies InMail.
A stored message or deliveredAt timestamp does not establish recipient delivery
or reading. LinkedIn hides the normal sent checkmark for InMail messages.
`

const messagesSendHelp = `Preview or send an approved LinkedIn message.

Usage:
  linkedin messages send <person> --text-file PATH [--send]

Options:
      --text-file PATH       File containing the exact UTF-8 plain text
      --send                 Actually send; default only previews
  -h, --help                 Show help

Show the exact recipient and text to the human, including all URLs, and obtain
explicit approval before using --send. LinkedIn does not interpret Markdown;
use plain URLs. The file text is preserved, including newlines.

New conversations check compose options. Confirmed free Open Profile InMail
is supported automatically and previews include free_inmail:true. No subject
is added. Paid, blocked, or unknown InMail stops without spending credits.

Both modes read recent messages first. An identical outgoing message in that
page returns already_present:true without sending. This check covers only
the fetched page, not the entire history. A send makes one POST with no retry.
After a timeout or unexpected receipt, read the conversation before retrying.
Do not test with live messages. Tests must use local fixtures or mocks.
HTTP failures include bounded, credential-redacted diagnostics on stderr.
`

const loginHelp = `Import your signed-in LinkedIn browser session with SweetCookie.

Usage:
  linkedin auth login [flags]

Options:
      --browser NAME         Browser to read (default: auto, first usable session)
      --profile NAME         Profile name, directory, or cookie database;
                             requires an explicit --browser
      --cookies-file PATH    Import cookie JSON instead of reading a browser;
                             cannot be combined with --browser or --profile
  -h, --help                 Show help

Examples:
  linkedin auth login
  linkedin auth login --browser chrome --profile 'Your Chrome'
  linkedin auth login --browser arc
  linkedin auth login --cookies-file /path/to/cookies.json

Browsers:
  chrome, chromium, edge, brave, vivaldi, opera, arc, helium, dia, comet, atlas,
  whale, firefox, zen, floorp, waterfox, librewolf, safari
  Availability depends on your operating system.

Sign in at linkedin.com in your browser first. Import may prompt for Keychain
or keyring access. Cookie JSON accepts an array or an object with a cookies array.
Both li_at and JSESSIONID must belong to the same browser profile.

Only those two cookies are saved. Output contains status, never cookie values.
`

const statusHelp = `Check whether saved LinkedIn cookies exist and have expired.

Usage:
  linkedin auth status

Options:
  -h, --help                 Show help

Prints JSON with authenticated, source, and available expiry information.
This is a local check; it does not verify the session with LinkedIn.
`

const logoutHelp = `Remove the local copy of your LinkedIn cookies.

Usage:
  linkedin auth logout

Options:
  -h, --help                 Show help

Prints JSON confirming removal. Your browser remains signed in.
`

func isHelpFlag(arg string) bool { return arg == "--help" || arg == "-h" }

func writeHelp(w io.Writer, commands []string) error {
	var help string
	switch topic := strings.Join(commands, " "); topic {
	case "":
		help = fmt.Sprintf(rootHelp, pageSize)
	case "companies":
		help = companiesHelp
	case "companies people":
		help = fmt.Sprintf(peopleHelp, pageSize, pageSize)
	case "people":
		help = profilesHelp
	case "people search":
		help = peopleSearchHelp
	case "people profile":
		help = profileHelp
	case "people mutuals":
		help = mutualsHelp
	case "people connect":
		help = connectHelp
	case "messages":
		help = messagesHelp
	case "messages read":
		help = messagesReadHelp
	case "messages send":
		help = messagesSendHelp
	case "posts", "posts create", "posts read", "posts edit", "posts comment", "posts comments":
		help = postsHelp
	case "articles", "articles read-draft", "articles delete-draft":
		help = articlesHelp
	case "api", "api capture":
		help = apiHelp
	case "analytics", "analytics summary", "analytics compare":
		help = analyticsHelp
	case "experience", "experience update":
		help = experienceHelp
	case "auth":
		help = authHelp
	case "auth login":
		help = loginHelp
	case "auth status":
		help = statusHelp
	case "auth logout":
		help = logoutHelp
	default:
		return fmt.Errorf("unknown help topic %q; run linkedin --help", topic)
	}
	_, err := io.WriteString(w, help)
	return err
}
