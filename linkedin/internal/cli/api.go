package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const apiHelp = `Usage:
  go run . api capture --save-response NEW_FILE URL

Read-only contract inspection. Accepts www.linkedin.com pages/API URLs, or
public static.licdn.com/aero-v1/sc/h/ assets observed in the website.
Cookies never accompany static assets. Saves raw data to a new private 0600
file. Does not execute scripts, follow redirects, or retry failures.
`

func runAPICapture(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		_, err := io.WriteString(out, apiHelp)
		return err
	}
	if args[0] != "capture" {
		return fmt.Errorf("api requires capture; run go run . api --help")
	}
	fs := flag.NewFlagSet("api capture", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("save-response", "", "new private capture path")
	positionals, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(out, apiHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 || *file == "" {
		return fmt.Errorf("api capture requires --save-response NEW_FILE and one URL")
	}
	u, err := url.Parse(positionals[0])
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("api capture requires a canonical HTTPS LinkedIn URL")
	}
	static := u.Host == "static.licdn.com" && strings.HasPrefix(u.Path, "/aero-v1/sc/h/") && u.RawQuery == ""
	if !static && u.Host != "www.linkedin.com" {
		return fmt.Errorf("unsupported capture host %q", u.Host)
	}
	f, err := os.OpenFile(*file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	var resp *http.Response
	if static {
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("read public asset: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("public asset returned HTTP %d; no retry", resp.StatusCode)
		}
	} else {
		c, err := newLinkedinClient(store)
		if err != nil {
			return err
		}
		resp, err = c.get(ctx, u.RequestURI(), nil)
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	limit := int64(16 << 20)
	if static {
		limit = 64 << 20
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("capture exceeds %d bytes", limit)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"saved": *file, "bytes": n, "status": resp.StatusCode, "content_type": resp.Header.Get("Content-Type")})
}
