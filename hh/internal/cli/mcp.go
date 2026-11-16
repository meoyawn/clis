package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	hh "github.com/meoyawn/clis/hh/internal/service"
	"golang.org/x/sync/errgroup"
)

type rpcRequest struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

func runMCP(ctx context.Context, args []string, profile, cookies string, headers http.Header) error {
	if len(args) == 0 || args[0] != "serve" {
		return fmt.Errorf("mcp requires serve; run hh --help")
	}
	flags := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	interval := flags.Duration("interval", 15*time.Second, "minimum time between reads of the same chat (1s..5m)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected mcp serve arguments %q", flags.Args())
	}
	if *interval < time.Second || *interval > 5*time.Minute {
		return fmt.Errorf("invalid interval %s: expected 1s through 5m", *interval)
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	base, _ := url.Parse("https://chatik.hh.ru")
	client, err := hh.BrowserClient(ctx, base, profile, cookies, headers)
	if err != nil {
		return err
	}
	return serveMCP(ctx, os.Stdin, os.Stdout, newChatEventSource(client, *interval))
}

var errMCPInputClosed = errors.New("MCP input closed")

// serveMCP owns both pipes. Closing them releases blocked reads and writes when
// the client disconnects or cancels the process, including an active HH read.
func serveMCP(ctx context.Context, input io.ReadCloser, output io.WriteCloser, source *chatEventSource) error {
	group, lifetime := errgroup.WithContext(ctx)
	var writing sync.Mutex
	send := func(id json.RawMessage, result any, failure *rpcError) error {
		frame := map[string]any{"jsonrpc": "2.0", "id": id}
		if failure != nil {
			frame["error"] = failure
		} else {
			frame["result"] = result
		}
		writing.Lock()
		defer writing.Unlock()
		if lifetime.Err() != nil {
			return lifetime.Err()
		}
		return json.NewEncoder(output).Encode(frame)
	}
	var requests sync.Mutex
	active := map[string]context.CancelFunc{}
	slots := make(chan struct{}, 32)
	group.Go(func() error {
		<-lifetime.Done()
		_ = input.Close()
		_ = output.Close()
		return nil
	})
	group.Go(func() error {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var request rpcRequest
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				code, message := -32700, "Parse error"
				if json.Valid(scanner.Bytes()) {
					code, message = -32600, "Invalid Request"
				}
				if err := send(nil, nil, &rpcError{Code: code, Message: message}); err != nil {
					return err
				}
				continue
			}
			key, valid := rpcKey(request.ID)
			if request.Version != "2.0" || request.Method == "" || !valid {
				if err := send(nil, nil, &rpcError{Code: -32600, Message: "Invalid Request"}); err != nil {
					return err
				}
				continue
			}
			if len(request.ID) == 0 {
				if request.Method == "notifications/cancelled" {
					var params struct {
						ID json.RawMessage `json:"requestId"`
					}
					if json.Unmarshal(request.Params, &params) == nil {
						cancelKey, valid := rpcKey(params.ID)
						if valid && cancelKey != "" {
							requests.Lock()
							if cancel := active[cancelKey]; cancel != nil {
								cancel()
							}
							requests.Unlock()
						}
					}
				}
				continue
			}
			requests.Lock()
			if _, exists := active[key]; exists {
				requests.Unlock()
				if err := send(request.ID, nil, &rpcError{Code: -32600, Message: "Request ID is already active"}); err != nil {
					return err
				}
				continue
			}
			select {
			case slots <- struct{}{}:
			default:
				requests.Unlock()
				if err := send(request.ID, nil, &rpcError{Code: -32025, Message: "Too many active requests", Data: map[string]any{"max": 32}}); err != nil {
					return err
				}
				continue
			}
			operation, cancel := context.WithCancel(lifetime)
			active[key] = cancel
			requests.Unlock()
			group.Go(func() error {
				defer func() { cancel(); <-slots }()
				result, failure := source.handle(operation, request)
				requests.Lock()
				delete(active, key)
				requests.Unlock()
				if operation.Err() != nil {
					return nil
				}
				return send(request.ID, result, failure)
			})
		}
		if err := scanner.Err(); err != nil && lifetime.Err() == nil {
			return fmt.Errorf("read MCP input: %w", err)
		}
		return errMCPInputClosed
	})
	err := group.Wait()
	if ctx.Err() != nil || errors.Is(err, errMCPInputClosed) {
		return nil
	}
	return err
}

func rpcKey(id json.RawMessage) (string, bool) {
	if len(id) == 0 {
		return "", true
	}
	if len(id) > 1024 {
		return "", false
	}
	if id[0] == '"' {
		var text string
		if json.Unmarshal(id, &text) == nil {
			return "s:" + text, true
		}
	}
	if number, err := strconv.ParseInt(string(id), 10, 64); err == nil {
		return "n:" + strconv.FormatInt(number, 10), true
	}
	return "", false
}
