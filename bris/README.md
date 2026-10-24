# bris

Submit cold water, day/night electricity and drinking water readings to
BRIS without a browser. Gas belongs to Госуслуги РТ and is ignored.

From this folder, with Go 1.27.2 or newer:

```console
go build .
./bris --dry-run /path/to/private/meters.yaml
./bris /path/to/private/meters.yaml
```

Flags go before the YAML path. Use `--credentials /path/to/private.json` to select
a credentials file; the default is `credentials.json` in the current directory.
Copy `credentials.example.json` to `credentials.json`, fill in `login`,
`password`, `account`, and the four `meters` serial numbers, and run
`chmod 600 credentials.json`. The credentials file and built
binary are gitignored. The CLI never logs credentials or saves session cookies.

Each run creates a fresh session and logs in with the saved credentials. If BRIS
redirects a submission to login, the CLI logs in again, refreshes the form and
retries once. An uncertain network failure is never automatically replayed.

`--dry-run` logs in and displays the live previous values, proposed values and
usage, without posting the meter form. It also prints historical consumption
from the YAML, so submitting the same readings again does not erase the usage
report. Last month's recorded usage includes its actual interval dates. A
missing previous month produces a reminder that you forgot to send readings;
the change across the entire recorded gap is shown with both dates and days.
A normal run submits all four values,
requires BRIS's acceptance message, then fetches the page again to verify the
stored readings and their month. Equal values are submitted too, allowing a
month with no consumption to be recorded. BRIS allows corrections on the same
day; repeated runs that day can resubmit the same values.

The latest dated YAML entry must be in the current calendar month in
`Europe/Moscow`, including the year, and cannot be in the future. Dates can be
`YYYY-MM-DD` or the legacy `DD-MM-YYYY`. All entries require nonnegative whole
numbers for `water`, `day`, `night` and `drinking`; cumulative readings cannot
decrease. `drinking` is already in litres in this YAML. `gas` is optional and is
never sent to BRIS. The input file is never modified.
The optional `uslugi` routing metadata is ignored by BRIS.

The CLI verifies the account and meter serials from your private configuration,
reading BRIS's internal IDs and form fields afresh on every run. Missing account
or meter configuration stops before login; an unexpected account or meter stops
before submission. Day and night normally use the same electricity meter serial.
The tariff mapping is:

| YAML field | Private configuration | Tariff |
| --- | --- | --- |
| water | `meters.water` | 1 |
| day | `meters.day` | 2 |
| night | `meters.night` | 3 |
| drinking | `meters.drinking` | 1 |

All values in `credentials.example.json` and the test fixtures are synthetic.
Keep your real configuration and readings outside version control.

Checks:

```console
moon run bris:check
```
