package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service"
)

type followResult struct {
	Account     domain.Profile      `json:"account"`
	Recipient   service.FollowState `json:"recipient"`
	CheckedAt   time.Time           `json:"checked_at"`
	AttemptPath string              `json:"attempt_path,omitempty"`
}

func followCommand(ctx context.Context, args []string, dir string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("people follow", flag.ContinueOnError)
	fs.SetOutput(errOut)
	send := fs.Bool("send", false, "follow the resolved account")
	expectAccount := fs.String("expect-account", "", "selected signed-in handle")
	expectID := fs.String("expect-id", "", "resolved recipient ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("people follow requires flags followed by one handle")
	}
	handle, err := domain.Handle(fs.Arg(0))
	if err != nil {
		return err
	}
	if *send && (*expectAccount == "" || *expectID == "") {
		return fmt.Errorf("--send requires --expect-account and --expect-id from the preview")
	}
	c, err := loadClient(dir)
	if err != nil {
		return err
	}
	owner, err := c.Bootstrap(ctx)
	if err != nil {
		return err
	}
	if *send && !strings.EqualFold(strings.TrimPrefix(*expectAccount, "@"), owner.Handle) {
		return fmt.Errorf("selected account does not match --expect-account %q", *expectAccount)
	}
	state, err := c.Following(ctx, handle)
	if err != nil {
		return err
	}
	result := followResult{Account: owner, Recipient: state, CheckedAt: time.Now().UTC()}
	if !*send {
		return writeJSON(out, result)
	}
	if state.Profile.ID != *expectID {
		return fmt.Errorf("recipient %q does not match --expect-id %q", handle, *expectID)
	}
	if state.Profile.ID == owner.ID || state.Blocking || state.BlockedBy {
		return fmt.Errorf("cannot follow self or a blocked relationship: %q", handle)
	}
	if state.Following || state.Requested {
		return writeJSON(out, result)
	}
	if err := c.PrepareWriteMetadata(ctx); err != nil {
		return err
	}
	history := filepath.Join(dir, "follows", owner.ID)
	if err := os.MkdirAll(history, 0700); err != nil {
		return err
	}
	path := filepath.Join(history, state.Profile.ID+".json")
	result.AttemptPath = path
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("follow attempt exists or cannot be saved at %q; inspect before retrying: %w", path, err)
	}
	writeErr := writeJSON(f, result)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	receipt, err := os.OpenFile(path+".receipt.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer receipt.Close()
	result.Recipient, err = c.Follow(ctx, state.Profile, receipt)
	if err != nil {
		return fmt.Errorf("follow unconfirmed; inspect %q before retrying: %w", path, err)
	}
	result.CheckedAt = time.Now().UTC()
	confirmed, err := os.OpenFile(path+".confirmed.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer confirmed.Close()
	if err := writeJSON(confirmed, result); err != nil {
		return err
	}
	return writeJSON(out, result)
}
