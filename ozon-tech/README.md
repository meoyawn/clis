# ozon-tech

Read the public Ozon Tech vacancy catalog through its browser API. Requires Go
and an existing Chrome session that can open https://ozon.tech/vacancies/.

```sh
go run . --help
go run . auth import
go run . auth status
go run . auth status --check
go run . vacancies
go run . vacancies --details --concurrency 4
go run . vacancy 3096
```

The default filters are `Разработка` and `Удалённая работа`. Override them with
`--direction` and `--work-format`. Every list command walks all 20-item pages
and validates the stable total, page sizes and unique IDs/UUIDs before writing
any JSON. `--details` fetches and validates every full description, preserving
list order. A changed total, duplicate, missing card, unsupported description
format or antibot response fails the command instead of emitting partial data.
An empty `work_formats` array is preserved even when returned for a format filter.

`auth import` uses sweetcookie to copy only `__Secure-ETC`, `__Secure-ab-group`
and `abt_data` from Chrome into an owner-only file under the OS config directory.
Account access/refresh tokens are excluded. Choose a profile with
`--browser-profile 'Profile 1'`, or provide a SweetCookie inline file with
`--cookies-file FILE`. All commands accept `--session-file FILE` and
`--timeout 3m`; place flags after the command and before a vacancy ID.

Only explicit import accesses Chrome/Keychain. `auth status` reports local cookie
availability, while `--check` also tests the live API; neither claims an account
login. Refresh the site in Chrome and import again if challenged. Cookies never
appear in command output. Public API contracts were verified in Chrome on
2026-11-09: `POST /p-api/ozon-tech/vacancy/list` with `filters`, `page`,
`page_limit`, and `GET /p-api/ozon-tech/vacancy/<id>`. No application actions.

The application workspace's `moon run ozon-sync` writes the catalog and Markdown
files using this CLI. Check this module with `moon run ozon-tech:check`.
