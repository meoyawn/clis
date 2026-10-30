# CLI monorepo

- Shell: `fish`.
- Goal: minimally bloated LLM context. JSON output must contain only data that
  varies with the request, response, or runtime state. Never emit constant
  metadata, fixed explanatory prose, or static capability/filter inventories.
  Put invariant semantics, units, limitations, and usage in `--help` or docs.
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
- Before handing off code changes, run `moon run <project>:check` locally for
  every affected CLI and fix failures. Run `moon run :check` when changing
  shared code, workspace dependencies, or CI/toolchain/lint configuration.
  These checks cover builds, race tests, and lint; report what ran and any
  blockers in the handoff.
- Ensure new test fixtures and other required files are included in Git and
  are not ignored (`git status --short` and `git check-ignore -v <path>`).
  Local checks can pass with ignored files that will be missing in CI; verify
  fixture changes from a clean checkout when needed.
- CI uses `moon ci` to select checks affected by changed files. Use the Go
  toolchain plugin and shared root `.golangci.yml`; keep Taskfiles and per-CLI
  lint configs out of this repo.
- Never commit credentials, browser captures, private exports, or real account
  data. Keep fixtures synthetic or limited to anonymous public catalog data.
- Never send live messages, applications, or meter readings to test a change;
  use local fixtures and mocks.
