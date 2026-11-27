// Package webview lets the system web engine (WKWebView on macOS, WebView2 on
// Windows, WebKitGTK on Linux) load Ozon so that Ozon's own anti-bot script
// runs unmodified in a real browser engine. The CLI only reads the cookies the
// engine received; it never computes or forges the check.
package webview

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// gate starts the application loop the first time a command needs it.
var gate struct {
	once    sync.Once
	request chan struct{}
	ready   chan struct{}
	mu      sync.Mutex
	err     error
}

var errUnavailable = errors.New("no system web view is available on this machine")

// Run runs command and, only if it asks for the web view, the native
// application loop on the calling goroutine, which must be the main one.
// It returns command's exit code.
func Run(command func() int) int {
	gate.request, gate.ready = make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	go func() { done <- command() }()
	select {
	case code := <-done:
		return code
	case <-gate.request:
	}
	if !mygo.WebViewAvailable() {
		gate.mu.Lock()
		gate.err = errUnavailable
		gate.mu.Unlock()
		close(gate.ready)
		return <-done
	}
	go func() { os.Exit(<-done) }()
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	var readyOnce sync.Once
	mygo.App.WhenReady(func() { readyOnce.Do(func() { close(gate.ready) }) })
	err := mygo.App.Run()
	if err == nil {
		err = errors.New("the web view application loop ended")
	}
	gate.mu.Lock()
	gate.err = err
	gate.mu.Unlock()
	readyOnce.Do(func() { close(gate.ready) })
	return <-done
}

func start(ctx context.Context) error {
	if gate.request == nil {
		return errors.New("web view used outside webview.Run")
	}
	gate.once.Do(func() { close(gate.request) })
	select {
	case <-gate.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.err
}

// Cookies loads origin in a hidden web view with a fresh in-memory session
// and returns that session's cookies for origin once the site has served a
// page to a navigation that already carried the cookie named ready. A
// challenge page, served before that cookie exists, never counts, so the
// result does not depend on how the site names or versions its scripts.
// Nothing is written to disk.
func Cookies(ctx context.Context, origin, ready string) ([]*http.Cookie, error) {
	if err := start(ctx); err != nil {
		return nil, err
	}
	session := mygo.NewEphemeralSession()
	// Events keep their order: a navigation start, then its finished load.
	const navigating, finished = 0, 1
	events := make(chan int, 64)
	failed := make(chan error, 1)
	send := func(e int) {
		select {
		case events <- e:
		default:
		}
	}
	var w *mygo.Window
	mygo.RunOnMain(func() {
		w = mygo.NewWindow(mygo.WindowOptions{Width: 1024, Height: 768, Hidden: true, SkipTaskbar: true,
			Page: mygo.PageOptions{Untrusted: true, Session: session}})
		page := w.Page()
		page.OnWillNavigate(func(*mygo.NavigateEvent) { send(navigating) })
		page.OnDidFinishLoad(func() { send(finished) })
		page.OnDidFailLoad(func(e *mygo.LoadError) {
			select {
			case failed <- e:
			default:
			}
		})
		if err := page.LoadURL(origin); err != nil {
			failed <- err
		}
	})
	defer mygo.RunOnMain(func() { w.Close() })
	carried := false
	for {
		var event int
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-failed:
			return nil, err
		case event = <-events:
		}
		list, err := session.Cookies.List(mygo.CookieFilter{URL: origin})
		if err != nil {
			return nil, err
		}
		has := false
		for _, c := range list {
			has = has || (c.Name == ready && c.Value != "")
		}
		if event == navigating {
			carried = has
			continue
		}
		if !carried || !has {
			continue
		}
		cookies := make([]*http.Cookie, 0, len(list))
		for _, c := range list {
			cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires, Secure: c.Secure, HttpOnly: c.HTTPOnly})
		}
		return cookies, nil
	}
}

// Timeout bounds one renewal; Ozon's check normally finishes in seconds.
const Timeout = 30 * time.Second
