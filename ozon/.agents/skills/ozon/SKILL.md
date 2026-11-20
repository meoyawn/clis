---
name: ozon
description: Use for Ozon tasks through the local Go CLI.
---

Do Ozon tasks through the CLI: run `go run . --help` from `/Users/meoyawn/Developer/clis/ozon/` and follow its help. Never invoke an installed `ozon` binary, send ad hoc HTTP requests or do the task in the browser. Use the fewest network-backed commands the task needs and reuse their output.

When the CLI lacks a capability or fails, fix it in that directory following the repository instructions, run `moon run ozon:check` and resume the original task through `go run .`. To learn a request the CLI doesn't make yet, inspect the real browser's HTTP traffic with the Chrome DevTools MCP. Keep cookies and captures out of Git, test with local fixtures, and never send live writes to test a change. Fixing a CLI never authorizes applications, messages, posts or account changes.

On a CAPTCHA, verification page or authentication challenge, stop without retrying and ask the human to clear it in Chrome; then follow the CLI's help to re-import the session and rerun the original command once. On a rate limit or access block, stop without retrying and report it.
