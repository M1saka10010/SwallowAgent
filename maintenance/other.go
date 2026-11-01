//go:build !linux

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type Manager struct{}

func New(enabled bool, scope string) (*Manager, error) { return &Manager{}, nil }
func (*Manager) Handle(string, json.RawMessage)        {}

type Sender interface {
	Send(context.Context, string, interface{}) error
}

func (*Manager) Run(ctx context.Context, c Sender) {
	for {
		_ = c.Send(ctx, "maintenance_sync", Batch{Events: []Event{}})
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func Worker(string) error { return fmt.Errorf("maintenance requires Linux systemd") }
