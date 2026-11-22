package tunnel

import (
	"context"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

func connectAgent(ctx context.Context) (net.Conn, error) {
	const pipePrefix = `\\.\pipe\`
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		socket = pipePrefix + "openssh-ssh-agent"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if strings.HasPrefix(strings.ToLower(socket), pipePrefix) {
		return winio.DialPipeContext(ctx, socket)
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", socket)
}
