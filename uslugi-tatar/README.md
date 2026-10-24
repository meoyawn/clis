# uslugi-tatar

A Go CLI for submitting gas, electricity and water meter readings to
[Госуслуги Республики Татарстан](https://uslugi.tatarstan.ru/).

- `gas`: submit a configured gas meter reading.
- `main`: submit electricity, hot water and cold water readings for a saved ЖКХ account.
- `auth import --browser <browser>`: import and verify your browser's portal session.
- `auth status`: check the imported session against the live portal.
- `accounts`: discover saved ЖКХ and gas accounts and suggest YAML routing.
- `--dry-run`: validate the input and compare with the portal without submitting readings.

Requires Go 1.27.2+ and an authenticated browser session for the portal.

## Install

```console
go install github.com/meoyawn/clis/uslugi-tatar@latest
```

Or build from source:

```console
git clone https://github.com/meoyawn/clis.git
cd clis/uslugi-tatar
go build -trimpath .
```

## Authenticate

Log in to the portal in your browser, then run:

```console
uslugi-tatar auth import --browser chrome
uslugi-tatar auth status
```

Import uses [SweetCookie](https://github.com/steipete/sweetcookie/) to read a
snapshot of that browser's local cookie database and decrypt its cookies using
the OS keychain where necessary. Only cookies matching the portal are imported;
the browser database is not modified. No password or manual cookie export is
needed. On macOS, access to Chrome Safe Storage may prompt via Keychain.

Supported browser names: `chrome`, `chromium`, `edge`, `brave`, `vivaldi`, `opera`,
`arc`, `helium`, `dia`, `comet`, `atlas`, `whale`, `firefox`, `zen`, `floorp`,
`waterfox`, `librewolf`, `safari`. Availability depends on the OS and installed
browser. Use `--profile Default` (or a profile directory/database path) on
`auth import` to select a particular profile.

The CLI verifies the imported session before saving it. Commands then use that
private session file without reading the browser on every run. Cookies are
stored as sensitive plaintext with file permissions `0600` in a `0700` directory:

- macOS: `~/Library/Application Support/uslugi-tatar-cli/auth.json`
- Linux: `${XDG_CONFIG_HOME:-~/.config}/uslugi-tatar-cli/auth.json`

`USLUGI_TATAR_CONFIG_DIR` overrides the dedicated config directory. Keep it
outside shared repositories. Cookie values are never printed. `auth status`
checks the live signed-in menu, exits nonzero for missing/expired/revoked
sessions, and reports request failures without claiming a successful check.
When a session stops working, log in in the browser and run `auth import` again.
Failed verification does not overwrite a previously saved session.

## Discover and configure

```console
uslugi-tatar accounts
uslugi-tatar accounts --account 0000000001 --yaml
uslugi-tatar accounts --account 000000000001 --yaml
```

`accounts` lists saved ЖКХ cards (including cards needing setup) and gas accounts,
with confirmed account numbers, addresses/apartments, informer UUIDs and gas
meter serials. `--yaml` prints suggested `uslugi.main` or `uslugi.gas` blocks for
copying into `meters.yaml`. Without `--account`, candidates are separate YAML
documents: choose the appropriate account rather than pasting the entire output
into one meter file. For one apartment, merge its chosen `main` and `gas` entries
under a single `uslugi` key. Display names are not used to guess which accounts
belong together. Unconfirmed or unsupported accounts have no generated routing.

Discovery does not submit readings, pay bills, save configuration or change the
default gas account. Selecting a gas account requires a workflow POST. The output
contains personal account details; keep it private. Recommendations are routing
identifiers; add your actual measured readings separately.


Copy `meters.example.yaml` to a private `meters.yaml` outside the checkout, or
use the ignored `meters.yaml` filename locally. All example accounts, addresses,
UUIDs, serials, dates and readings are invented. Replace them with your own
saved-account identifiers and a reading dated in the current Moscow month.

Routing comes from the YAML contents, independently of the file name:

```yaml
uslugi:
  main:
    account: "0000000001"
    flat: "1"
    address: "ул ПРИМЕРНАЯ, дом 1"
    informer: "00000000-0000-4000-8000-000000000001"
  gas:
    account: "000000000001"
    meter: "100001"

2000-10-18:
  electricity: 1100
  hot: 110
  cold: 200
  gas: 123
```

For `gas`, use the 12-digit account and meter serial from your saved gas account.
The CLI selects that account, obtains its initials from the portal, and checks
the displayed account and meter before submitting. It preserves your default
saved-account preference.

For `main`, use your 10-digit ЖКХ account, apartment number, expected street/house
text and the informer UUID. The UUID is the `informer_id` parameter in the saved
ЖКХ card's `/personal-account/init-by-user-tsj` URL, discovered by `accounts`.
The CLI checks the account,
address and apartment before opening its meters. Supported services are:

| YAML field | Portal service |
| --- | --- |
| electricity | Электроснабжение день |
| hot | Подогрев воды |
| cold | Холодное водоснабжение |

Gas and main routing are independent; each command requires only its own routing
and meter fields. Other supported meter fields may remain in the same file.

## Run

Flags go before the YAML path:

```console
uslugi-tatar gas --dry-run /path/to/private/meters.yaml
uslugi-tatar main --dry-run /path/to/private/meters.yaml
uslugi-tatar gas /path/to/private/meters.yaml
uslugi-tatar main /path/to/private/meters.yaml
```

## Validation and submission

The latest dated entry must be in the current `Europe/Moscow` calendar month
and cannot be in the future. Dates support `YYYY-MM-DD` and legacy `DD-MM-YYYY`.
Readings must be nonnegative whole YAML integers. Cumulative readings cannot
decrease against either local history or live portal values. Gas values must
fit six digits; main meter capacities are obtained from the portal.

Both commands report consumption since the preceding recorded reading. Missing
months produce a reminder and the consumption across the entire recorded gap,
including its date range and duration. Input files are never modified.

A dry run selects the account and opens its live meter form, then prints the
proposed changes. It never posts meter readings, though selecting a service can
require a workflow POST.

A normal `gas` run posts all six digits and the hidden total, requires the portal's
success acknowledgement, then separately reads back the last transmitted value.
Equal gas readings can be resubmitted. The YAML reading date is sent with gas;
the main form uses billing dates supplied by the portal.

A normal `main` run skips submission when all three values already match. When
values change, it preserves the original hidden fields, serialized `item[]`
metadata and fresh CSRF/session fields, then separately reads back the values
and meter serials. Neither command makes payments. Uncertain submissions are
never automatically retried; inspect the live values with a dry run before
retrying.

## Development

```console
go test -race ./...
moon run uslugi-tatar:lint
```

Tests run against local HTTP servers and entirely synthetic fixtures. Keep
personal meter files, browser exports, HAR captures and request logs out of
commits. Use invented accounts, addresses, UUIDs, cookies and readings in any
new tests or documentation.

Licensed under MIT. This project is not affiliated with the portal operator.
