# CLIs

A set of useful Go CLIs.

| CLI | Purpose |
| --- | --- |
| [avito](avito/) | Classified listings and seller details |
| [bris](bris/) | Utility meter readings |
| [hh](hh/) | Jobs, resumes, and applications |
| [invitro](invitro/) | Medical test catalog and promotions |
| [linkedin](linkedin/) | Profiles, connections, messages, text posts, and private analytics |
| [lemana](lemana/) | Lemana PRO catalog, characteristics, reviews and Kazan stock |
| [santehnika](santehnika/) | Santehnika Online catalog and product details |
| [twitter](twitter/) | X posts, native images, own-post replies, and engagement counters |
| [megastroy](megastroy/) | Home improvement catalog |
| [ozon](ozon/) | Products, reviews, and shopping cart |
| [planeta](planeta/) | Pharmacy catalog and availability |
| [uslugi-tatar](uslugi-tatar/) | Tatarstan utility accounts and meter readings |

Run any CLI from the repository root:

```sh
go run ./<cli> --help
```

Inside a CLI directory, use `go run . --help`. Each CLI has the same entrypoint
layout: `main.go` delegates to `internal/cli`. Each directory is an independent
Go module, connected through `go.work`.

[Moon](https://moonrepo.dev/) 2.6.2 or newer manages the shared build tasks.
Its [Go toolchain plugin](https://github.com/moonrepo/plugins/tree/master/toolchains/go)
installs Go 1.27.2 and golangci-lint 2.14.0.

```sh
moon setup              # install the toolchain
moon run :build         # build into bin/<cli>
moon run :test          # run tests with the race detector
moon run :lint          # run the shared golangci-lint configuration
moon run :check         # build, lint, and test
moon run ozon:check     # check one CLI
moon run :install      # install into GOBIN or GOPATH/bin
```

For a standalone install, use `go install github.com/meoyawn/clis/<cli>@latest`.
Module READMEs describe service setup and usage. All modules share
[`.golangci.yml`](.golangci.yml); run `moon run :test` to test the workspace,
or `go test ./...` from a CLI directory.

Keep credentials, account state, captures, and private exports outside Git.
Tests use synthetic fixtures or anonymous public catalog data.
