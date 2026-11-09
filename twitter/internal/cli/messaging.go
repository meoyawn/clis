package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service"
)

func checkMessaging(ctx context.Context, args []string, dir string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("messages check requires one recipient handle; got %d arguments", len(args))
	}
	handle, err := domain.Handle(args[0])
	if err != nil {
		return err
	}
	c, err := loadClient(dir)
	if err != nil {
		return err
	}
	owner, err := c.Bootstrap(ctx)
	if err != nil {
		return err
	}
	availability, err := c.MessagingAvailability(ctx, handle)
	if err != nil {
		return err
	}
	return writeJSON(out, struct {
		Account domain.Profile `json:"account"`
		service.MessagingAvailability
		CheckedAt time.Time `json:"checked_at"`
	}{Account: owner, MessagingAvailability: availability, CheckedAt: time.Now().UTC()})
}
