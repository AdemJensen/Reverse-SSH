//go:build !windows

package tunnel

import (
	"context"
	"errors"
	"net"
	"os"
	"time"
)

func connectAgent(ctx context.Context) (net.Conn, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, errors.New("未设置 SSH_AUTH_SOCK")
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	return dialer.DialContext(ctx, "unix", socket)
}
