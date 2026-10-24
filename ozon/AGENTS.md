# Repository instructions

## Environment

- Shell: `fish`.

## Git

- Never undo unrelated worktree changes; they belong to concurrent work.

## Approvals

- When multiple commands need the same new approval prefix, request and finish
  the first approval before launching the remaining commands. Never parallelize
  approval-gated commands until the reusable prefix rule is stored.

## Mandatory local CLI installation after bug fixes

- After **every bug fix**, run `go install -trimpath .` from the monorepo's
  `ozon/` directory **before reporting completion**. The installed local `ozon` must include
  the fix.
- `go run`, `go build`, and passing tests do **not** replace this installation.
- Verify that `ozon` on `PATH` resolves to the installed binary in `GOBIN`, or
  `GOPATH/bin` when `GOBIN` is unset. Smoke-test that installed binary with the
  commands relevant to the fix.
- Do not leave the user running a stale local CLI or ask them to install a bug
  fix that you can install yourself.
