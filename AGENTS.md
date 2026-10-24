# CLI monorepo

- Shell: `fish`.
- Never undo unrelated worktree changes; they belong to concurrent work.
- When multiple commands need the same new approval prefix, finish the first
  approval before launching the remaining commands.
- Each CLI directory is a Go module at `github.com/meoyawn/clis/<cli>` in
  `go.work`. Read its own instructions before making changes.
- Use the same layout: a root `main.go` calls `internal/cli.Main`. Keep command
  code and its tests in `internal/cli`, separate service packages in
  `internal/service`, and domain types in `internal/domain`. Keep fixtures beside
  the tests that use them.
- Run `go run ./<cli> --help` from the workspace root, or `go run . --help` from
  a CLI directory. Follow the service's skill and discover flags through help.
- Run `moon run :check` for all CLI builds, race tests, and lint, or
  `moon run <project>:check` for one CLI. Use the Go toolchain plugin and shared
  root `.golangci.yml`; keep Taskfiles and per-CLI lint configs out of this repo.
- Never commit credentials, browser captures, private exports, or real account
  data. Keep fixtures synthetic or limited to anonymous public catalog data.
- Never send live messages, applications, or meter readings to test a change;
  use local fixtures and mocks.
