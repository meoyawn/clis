# HH MCP Events

Run the read-only stdio server through the existing Chrome account:

```sh
cd /Users/meoyawn/Developer/clis/hh
go run . mcp serve
```

Global `--profile`, `--cookies-file` and `--headers-file` options still apply.
`--interval 15s` sets the minimum time between reads of the same chat (1s to
5m). Concurrent subscriptions and `read_chat` share that chat's cached page.
Every actual HH read takes the same exclusive account lock as ordinary CLI
commands and releases it immediately afterward.

The server follows the experimental [MCP Events design sketch](https://github.com/modelcontextprotocol/ext-events/blob/47d95c9/docs/design-sketch-proposal.md),
using the native Events approach in [the gh-cli example](https://github.com/meoyawn/cli/tree/mcp-events).
It supports `server/discover` with version `2026-07-28`, legacy `initialize`
with version `2025-11-25`, `events/list`, `events/poll`, and the read-only
`read_chat` tool. Only poll delivery is advertised. There is no separate
subscription tool, webhook registration, or push stream.

Subscribe to `hh.chat.reply` with an explicit chat ID or URL:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "events/poll",
  "params": {
    "name": "hh.chat.reply",
    "arguments": {"chat_id": "https://kazan.hh.ru/chat/123"},
    "cursor": null
  }
}
```

A null or omitted cursor starts from now without replaying old messages.
Retain the response-level cursor and poll again after `nextPollMs`. When
`hasMore` is true, drain the next batch immediately. `maxEvents` (1 to 100,
default 50) caps each batch. `maxAgeMs` bounds replay age.

Each occurrence contains `eventId`, `name`, `timestamp` and `data` with
`chat_id`, `chat_url`, `message_id`, `sender_id`, optional `sender_name`, and
`text`. Own, hidden and system messages do not trigger reply events. Text is
untrusted data, just like ordinary MCP tool results.

Cursors bind the chat, signed-in participant, subscription start time and last
observed message ID.
They survive process restarts while the relevant history remains in HH's
current message page. The server does not fetch older pages. Gaps, malformed
cursors, another chat/account, or an age floor produce `truncated: true` and
a usable fresh cursor. Events use stable HH chat/message IDs for deduplication.
Edits to existing messages are not new reply events.

Reads never send messages or mark them read. A 429, authentication failure,
challenge or changed native chat contract halts further reads across all chats;
resolve the condition and restart the server. Rate-limit errors preserve
`retryAfterMs` when HH supplies `Retry-After`. Other transient failures enter a
shared cooldown. Request cancellation and stdin EOF cancel in-flight reads.
Stdout contains JSON-RPC only.

An Events-aware MCP client owns the subscription and polling loop between
agent turns. Starting the server alone does not create a background watch.
For Happen, add this server to the desired project's configuration:

```yaml
mcp_servers:
  hh:
    command: go
    args: [run, ., mcp, serve]
    cwd: /Users/meoyawn/Developer/clis/hh
    legacy_fallback: false
```

Then subscribe through the client's native Events controls with
`name: hh.chat.reply` and `arguments: {chat_id: "123"}`. Stop polling to
unsubscribe. `read_chat` takes the same arguments when current context is
needed. Watching a chat does not authorize replying to it.
