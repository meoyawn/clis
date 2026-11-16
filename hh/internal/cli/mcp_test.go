package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	hh "github.com/meoyawn/clis/hh/internal/service"
	"golang.org/x/sync/errgroup"
)

type mcpPeer struct {
	in       *io.PipeWriter
	out      *json.Decoder
	sequence int
}

type rpcFrame struct {
	ID     json.RawMessage `json:"id"`
	Result map[string]any  `json:"result"`
	Error  *rpcError       `json:"error"`
}

func openMCPPeer(t *testing.T, source *chatEventSource) *mcpPeer {
	t.Helper()
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	group := &errgroup.Group{}
	group.Go(func() error { return serveMCP(ctx, input, output, source) })
	t.Cleanup(func() {
		_ = writer.Close()
		cancel()
		if err := group.Wait(); err != nil {
			t.Errorf("MCP shutdown: %v", err)
		}
		_ = reader.Close()
	})
	return &mcpPeer{in: writer, out: json.NewDecoder(reader)}
}

func (p *mcpPeer) send(t *testing.T, value any) {
	t.Helper()
	if err := json.NewEncoder(p.in).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func (p *mcpPeer) receive(t *testing.T) rpcFrame {
	t.Helper()
	var frame rpcFrame
	if err := p.out.Decode(&frame); err != nil {
		t.Fatalf("read JSON-RPC response: %v", err)
	}
	return frame
}

func (p *mcpPeer) call(t *testing.T, method string, params map[string]any) rpcFrame {
	t.Helper()
	fields := map[string]any{}
	if method != "initialize" {
		fields["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
	}
	for key, value := range params {
		fields[key] = value
	}
	p.sequence++
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": p.sequence, "method": method, "params": fields})
	frame := p.receive(t)
	if string(frame.ID) != fmt.Sprint(p.sequence) {
		t.Fatalf("response is not for request %d: %+v", p.sequence, frame)
	}
	return frame
}

func (p *mcpPeer) poll(t *testing.T, chat string, cursor any, extras map[string]any) map[string]any {
	t.Helper()
	params := map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": chat}, "cursor": cursor}
	for key, value := range extras {
		params[key] = value
	}
	frame := p.call(t, "events/poll", params)
	if frame.Error != nil {
		t.Fatalf("poll failed: %+v", frame.Error)
	}
	if frame.Result["resultType"] != "complete" {
		t.Fatalf("poll did not return a complete MCP result: %+v", frame)
	}
	return frame.Result
}

type chatFixture struct {
	mu      sync.Mutex
	pages   map[string]map[string]any
	reads   int
	status  int
	invalid bool
	client  *hh.Client
}

func newChatFixture(t *testing.T) *chatFixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := &chatFixture{pages: map[string]map[string]any{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		query := url.Values{"without_list": {"1"}, "platform": {"xhh"}, "theme": {"hh-day"}, "dest": {"iframe"}, "microphone": {"true"}}
		if r.Method != http.MethodGet || r.ContentLength > 0 || r.URL.Query().Encode() != query.Encode() || r.Header.Get("Accept") != "text/html" {
			t.Errorf("changed native read contract or attempted a write: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected request", 500)
			return
		}
		if f.status != 0 {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(f.status)
			fmt.Fprint(w, `{"error":"private-upstream-sentinel"}`)
			return
		}
		if f.invalid {
			fmt.Fprint(w, `<html>private-auth-sentinel</html>`)
			return
		}
		page := f.pages[strings.TrimPrefix(r.URL.Path, "/chat/")]
		data, _ := json.Marshal(map[string]any{"chatData": map[string]any{"chat": page}})
		fmt.Fprintf(w, `<template class="Chatik-InitialState">%s</template>`, html.EscapeString(string(data)))
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	f.client = hh.NewClient(server.Client(), base)
	f.set("123", []any{chatMessage("10", "900", "old reply"), chatMessage("20", "800", "own message")}, false)
	return f
}

func (f *chatFixture) set(id string, messages []any, more bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, value := range messages {
		value.(map[string]any)["chatId"] = id
	}
	f.pages[id] = map[string]any{"id": id, "currentParticipantId": "800", "messages": map[string]any{"items": messages, "hasMore": more}}
}

func chatMessage(id, author, text string) map[string]any {
	return map[string]any{"id": id, "chatId": "123", "participantId": author, "hidden": false, "type": "SIMPLE", "text": text,
		"creationTime": time.Now().Format(time.RFC3339Nano), "participantDisplay": map[string]any{"name": "Example recruiter"}}
}

func TestMCPChatReplyDeliveryAndReplay(t *testing.T) {
	f := newChatFixture(t)
	p := openMCPPeer(t, newChatEventSource(f.client, 0))
	discovery := p.call(t, "server/discover", map[string]any{})
	if discovery.Error != nil || discovery.Result["supportedVersions"].([]any)[0] != "2026-07-28" {
		t.Fatalf("native MCP discovery failed: %+v", discovery)
	}
	capabilities, _ := discovery.Result["capabilities"].(map[string]any)
	extensions, _ := capabilities["extensions"].(map[string]any)
	settings, offered := extensions["io.modelcontextprotocol/events"].(map[string]any)
	if !offered || settings["listChanged"] == true || capabilities["events"] != nil || discovery.Result["resultType"] != "complete" {
		t.Fatalf("SEP-3415 capability negotiation failed: %+v", discovery)
	}
	listed := p.call(t, "events/list", map[string]any{})
	if listed.Error != nil || listed.Result["resultType"] != "complete" || listed.Result["events"].([]any)[0].(map[string]any)["name"] != "hh.chat.reply" {
		t.Fatalf("reply event is not discoverable: %+v", listed)
	}
	start := p.poll(t, "https://kazan.hh.ru/chat/123", nil, nil)
	if len(start["events"].([]any)) != 0 || start["truncated"] != false {
		t.Fatalf("bootstrap replayed old messages: %v", start)
	}
	hidden := chatMessage("40", "900", "hidden reply")
	hidden["hidden"] = true
	system := chatMessage("70", "900", "system update")
	system["type"] = "SYSTEM"
	f.set("123", []any{chatMessage("60", "900", "second reply"), chatMessage("50", "800", "own follow-up"), hidden,
		chatMessage("30", "900", "first reply"), system, chatMessage("10", "900", "old reply"), chatMessage("20", "800", "own message")}, false)
	first := p.poll(t, "123", start["cursor"], map[string]any{"maxEvents": 1})
	firstEvent := first["events"].([]any)[0].(map[string]any)
	if first["hasMore"] != true || firstEvent["eventId"] != "hh_123_30" || firstEvent["data"].(map[string]any)["text"] != "first reply" {
		t.Fatalf("partial batch order or cursor lost: %v", first)
	}
	second := p.poll(t, "123", first["cursor"], nil)
	secondEvents := second["events"].([]any)
	if len(secondEvents) != 1 || secondEvents[0].(map[string]any)["eventId"] != "hh_123_60" || second["hasMore"] != false {
		t.Fatalf("own, hidden or system messages leaked into replies, or batch was skipped: %v", second)
	}
	quiet := p.poll(t, "123", second["cursor"], nil)
	if len(quiet["events"].([]any)) != 0 || quiet["truncated"] != false {
		t.Fatalf("completed batch repeated: %v", quiet)
	}
	restarted := openMCPPeer(t, newChatEventSource(f.client, 0))
	replayed := restarted.poll(t, "123", first["cursor"], nil)
	if len(replayed["events"].([]any)) != 1 || replayed["events"].([]any)[0].(map[string]any)["eventId"] != "hh_123_60" {
		t.Fatalf("cursor or event identity did not survive restart: %v", replayed)
	}
	read := restarted.call(t, "tools/call", map[string]any{"name": "read_chat", "arguments": map[string]any{"chat_id": "123"}})
	if read.Error != nil || read.Result["resultType"] != "complete" || len(read.Result["structuredContent"].(map[string]any)["messages"].([]any)) != 6 {
		t.Fatalf("read_chat lost visible context or exposed hidden text: %+v", read)
	}
}

func TestMCPChatCursorGaps(t *testing.T) {
	f := newChatFixture(t)
	p := openMCPPeer(t, newChatEventSource(f.client, 0))
	start := p.poll(t, "123", nil, nil)
	f.set("123", []any{chatMessage("20", "800", "own message"), chatMessage("30", "900", "reply that will expire")}, false)
	// Let the new reply cross the zero-millisecond replay age floor.
	<-time.After(5 * time.Millisecond)
	expired := p.poll(t, "123", start["cursor"], map[string]any{"maxAgeMs": 0})
	if expired["truncated"] != true || len(expired["events"].([]any)) != 0 {
		t.Fatalf("maxAgeMs did not bound replay independently of page gaps: %v", expired)
	}
	f.set("123", []any{chatMessage("40", "900", "recent reply")}, true)
	recent := p.poll(t, "123", start["cursor"], nil)
	if recent["truncated"] != true || len(recent["events"].([]any)) != 1 || recent["events"].([]any)[0].(map[string]any)["eventId"] != "hh_123_40" {
		t.Fatalf("missing native history was not signalled: %v", recent)
	}
	for _, cursor := range []any{"not-a-cursor", "", recent["cursor"]} {
		chat := "123"
		if cursor == recent["cursor"] {
			chat = "456"
			f.set(chat, []any{chatMessage("90", "900", "other conversation")}, false)
		}
		result := p.poll(t, chat, cursor, nil)
		if result["truncated"] != true || len(result["events"].([]any)) != 0 {
			t.Fatalf("invalid or cross-chat cursor silently replayed: %v", result)
		}
	}
}

func TestMCPEventRequestValidation(t *testing.T) {
	f := newChatFixture(t)
	p := openMCPPeer(t, newChatEventSource(f.client, 0))
	// Build the webhook fixture from 24 zero bytes instead of storing a token.
	syntheticWebhookSecret := "whsec_" + base64.StdEncoding.EncodeToString(make([]byte, 24))
	for _, test := range []struct {
		method string
		params map[string]any
		code   int
		data   map[string]any
	}{
		{"events/poll", map[string]any{"name": "unknown", "arguments": map[string]any{"chat_id": "123"}}, -32023, map[string]any{"kind": "event"}},
		{"events/poll", map[string]any{"arguments": map[string]any{"chat_id": "123"}}, -32602, nil},
		{"events/poll", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "https://hh.ru.evil.invalid/chat/123"}}, -32602, nil},
		{"events/poll", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "123", "extra": true}}, -32602, nil},
		{"events/poll", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{}}, -32602, nil},
		{"events/poll", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "123"}, "maxAgeMs": -1}, -32602, nil},
		{"events/poll", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "123"}, "maxEvents": 0}, -32602, nil},
		{"events/stream", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "123"}}, -32026, map[string]any{"feature": "deliveryMode", "value": "push"}},
		{"events/stream", map[string]any{"name": "unknown", "arguments": map[string]any{"chat_id": "123"}}, -32023, map[string]any{"kind": "event"}},
		{"events/stream", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{}}, -32602, nil},
		{"events/subscribe", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "123"}, "delivery": map[string]any{"mode": "webhook", "url": "https://callback.example.test/events", "secret": syntheticWebhookSecret}}, -32024, nil},
		{"events/unsubscribe", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "123"}, "delivery": map[string]any{"url": "https://callback.example.test/events"}}, -32024, nil},
	} {
		frame := p.call(t, test.method, test.params)
		if frame.Error == nil || frame.Error.Code != test.code {
			t.Errorf("%s %v: expected error %d, got %+v", test.method, test.params, test.code, frame)
			continue
		}
		for key, value := range test.data {
			if frame.Error.Data[key] != value {
				t.Errorf("%s lost error discriminator %s=%v: %+v", test.method, key, value, frame.Error)
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads != 0 {
		t.Fatalf("rejected subscriptions made HH requests: %d", f.reads)
	}
}

func TestMCPFramingAndLegacyDiscovery(t *testing.T) {
	f := newChatFixture(t)
	p := openMCPPeer(t, newChatEventSource(f.client, 0))
	if _, err := io.WriteString(p.in, "{invalid}\n"); err != nil {
		t.Fatal(err)
	}
	if frame := p.receive(t); frame.Error == nil || frame.Error.Code != -32700 || string(frame.ID) != "null" {
		t.Fatalf("malformed JSON did not produce a JSON-RPC parse error: %+v", frame)
	}
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": nil, "method": "ping"})
	if frame := p.receive(t); frame.Error == nil || frame.Error.Code != -32600 {
		t.Fatalf("invalid MCP request ID was accepted: %+v", frame)
	}
	p.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	legacy := p.call(t, "initialize", map[string]any{"protocolVersion": "2025-11-25"})
	if legacy.Error != nil || legacy.Result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("framing recovery or legacy discovery failed: %+v", legacy)
	}
	capabilities, _ := legacy.Result["capabilities"].(map[string]any)
	if capabilities["events"] != nil || capabilities["extensions"] != nil || legacy.Result["resultType"] != nil {
		t.Fatalf("legacy initialization advertised modern Events: %+v", legacy)
	}
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": "legacy-tools", "method": "tools/list"})
	if frame := p.receive(t); frame.Error != nil || len(frame.Result["tools"].([]any)) != 1 || frame.Result["resultType"] != nil {
		t.Fatalf("legacy tools stopped working: %+v", frame)
	}
	for _, method := range []string{"events/list", "events/poll"} {
		p.send(t, map[string]any{"jsonrpc": "2.0", "id": method, "method": method, "params": map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "123"}}})
		if frame := p.receive(t); frame.Error == nil || frame.Error.Code != -32601 {
			t.Fatalf("legacy request used the modern Events extension: %+v", frame)
		}
	}
	if frame := p.call(t, "ping", nil); frame.Error != nil || frame.Result["resultType"] != "complete" {
		t.Fatalf("modern request inherited legacy behavior: %+v", frame)
	}
}

func TestMCPRequestMetadata(t *testing.T) {
	p := openMCPPeer(t, newChatEventSource(nil, 0))
	for _, test := range []struct {
		name string
		meta any
		code int
	}{
		{"unsupported-version", map[string]any{"io.modelcontextprotocol/protocolVersion": "2099-01-01", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}, -32022},
		{"missing-version", map[string]any{"io.modelcontextprotocol/clientCapabilities": map[string]any{}}, -32602},
		{"invalid-version", map[string]any{"io.modelcontextprotocol/protocolVersion": 123, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}, -32602},
		{"missing-capabilities", map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"}, -32602},
		{"null-capabilities", map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": nil}, -32602},
		{"invalid-capabilities", map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": []any{}}, -32602},
		{"invalid-meta", []any{}, -32602},
		{"missing-meta", nil, -32602},
	} {
		t.Run(test.name, func(t *testing.T) {
			frame := p.call(t, "ping", map[string]any{"_meta": test.meta})
			if frame.Error == nil || frame.Error.Code != test.code {
				t.Fatalf("expected error %d, got %+v", test.code, frame)
			}
			if test.code == -32022 {
				supported, _ := frame.Error.Data["supported"].([]any)
				if len(supported) != 1 || supported[0] != "2026-07-28" || frame.Error.Data["requested"] != "2099-01-01" {
					t.Fatalf("version mismatch did not tell the client how to retry: %+v", frame.Error)
				}
			}
		})
	}
	if frame := p.call(t, "ping", nil); frame.Error != nil || frame.Result["resultType"] != "complete" {
		t.Fatalf("valid modern request failed after metadata errors: %+v", frame)
	}
}

func TestMCPSharedChatCacheAndAccountLock(t *testing.T) {
	f := newChatFixture(t)
	p := openMCPPeer(t, newChatEventSource(f.client, time.Hour))
	start := p.poll(t, "123", nil, nil)
	p.poll(t, "https://hh.ru/chat/123", start["cursor"], nil)
	read := p.call(t, "tools/call", map[string]any{"name": "read_chat", "arguments": map[string]any{"chat_id": "123"}})
	f.mu.Lock()
	reads := f.reads
	f.mu.Unlock()
	if read.Error != nil || reads != 1 {
		t.Fatalf("subscriptions and tools did not share the observation: reads=%d error=%v", reads, read.Error)
	}
	unlock, err := lockAccount()
	if err != nil {
		t.Fatalf("MCP retained the account lock between polls: %v", err)
	}
	f.set("456", []any{}, false)
	blocked := p.call(t, "events/poll", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "456"}})
	unlock()
	if blocked.Error == nil || blocked.Error.Code != -32025 || blocked.Error.Data["limit"] != "accountLock" {
		t.Fatalf("poll did not respect the other command's account lock: %+v", blocked)
	}
	p.poll(t, "456", nil, nil)
}

func TestMCPCachedBootstrapDoesNotReplayEarlierReplies(t *testing.T) {
	f := newChatFixture(t)
	p := openMCPPeer(t, newChatEventSource(f.client, time.Hour))
	read := p.call(t, "tools/call", map[string]any{"name": "read_chat", "arguments": map[string]any{"chat_id": "123"}})
	if read.Error != nil {
		t.Fatal(read.Error)
	}
	f.set("123", []any{chatMessage("10", "900", "old reply"), chatMessage("20", "800", "own message"), chatMessage("30", "900", "arrived before subscription")}, false)
	start := p.poll(t, "123", nil, nil)
	reconnected := openMCPPeer(t, newChatEventSource(f.client, 0))
	result := reconnected.poll(t, "123", start["cursor"], nil)
	if len(result["events"].([]any)) != 0 {
		t.Fatalf("a cached bootstrap replayed a reply from before subscription: %v", result)
	}
}

func TestMCPStopsAfterAccessOrContractFailure(t *testing.T) {
	for _, scenario := range []string{"rate-limit", "forbidden", "challenge", "wrong-chat", "foreign-message"} {
		t.Run(scenario, func(t *testing.T) {
			f := newChatFixture(t)
			f.mu.Lock()
			switch scenario {
			case "rate-limit":
				f.status = 429
			case "forbidden":
				f.status = 403
			case "challenge":
				f.invalid = true
			case "wrong-chat":
				f.pages["123"]["id"] = "456"
			case "foreign-message":
				f.pages["123"]["messages"].(map[string]any)["items"].([]any)[0].(map[string]any)["chatId"] = "456"
			}
			f.mu.Unlock()
			p := openMCPPeer(t, newChatEventSource(f.client, 0))
			for _, chat := range []string{"123", "456"} {
				frame := p.call(t, "events/poll", map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": chat}})
				encoded, _ := json.Marshal(frame)
				if frame.Error == nil || frame.Error.Data["restartRequired"] != true || strings.Contains(string(encoded), "sentinel") {
					t.Fatalf("failure did not halt reads or exposed upstream content: %s", encoded)
				}
				if scenario == "rate-limit" && (frame.Error.Code != -32025 || frame.Error.Data["limit"] != "upstreamRequests" || frame.Error.Data["retryAfterMs"] != float64(60000)) {
					t.Fatalf("Retry-After lost: %+v", frame.Error)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.reads != 1 {
				t.Fatalf("halted source retried HH automatically: %d", f.reads)
			}
		})
	}
}

func TestMCPRequestCancellationAndDisconnect(t *testing.T) {
	for _, scenario := range []string{"cancel", "disconnect"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			started, cancelled := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
				close(cancelled)
			}))
			t.Cleanup(server.Close)
			base, _ := url.Parse(server.URL)
			p := openMCPPeer(t, newChatEventSource(hh.NewClient(server.Client(), base), 0))
			p.send(t, map[string]any{"jsonrpc": "2.0", "id": "pending", "method": "events/poll", "params": map[string]any{"name": "hh.chat.reply", "arguments": map[string]any{"chat_id": "123"}, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}})
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("HH request did not start")
			}
			if scenario == "disconnect" {
				_ = p.in.Close()
			} else {
				p.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": "pending"}})
				p.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
				ping := p.call(t, "ping", map[string]any{})
				if ping.Error != nil {
					t.Fatalf("server did not remain usable after cancellation: %+v", ping)
				}
			}
			select {
			case <-cancelled:
			case <-time.After(3 * time.Second):
				t.Fatal("request cancellation or stdin EOF did not cancel the HH read")
			}
		})
	}
}
