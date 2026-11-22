package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"

	"golang.org/x/crypto/ssh"
)

type connectedSession struct {
	ctx    context.Context
	client *ssh.Client
}

func (m *Manager) setConnected(ctx context.Context, client *ssh.Client, remoteAddresses []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = &connectedSession{ctx: ctx, client: client}
	m.status = Status{
		State:           StateConnected,
		Message:         "反向隧道已建立",
		RemoteAddress:   remoteAddresses[0],
		RemoteAddresses: remoteAddresses,
	}
}

// DialRemote opens a TCP connection to an active reverse listener from the SSH server.
// The caller supplies the listener's configured address and owns the resulting connection.
func (m *Manager) DialRemote(ctx context.Context, host string, port int) (net.Conn, error) {
	host = normalizeForwardHost(host)
	remoteAddress := address(host, port)
	m.mu.Lock()
	connected := m.connected
	allowed := slices.Contains(m.status.RemoteAddresses, remoteAddress)
	m.mu.Unlock()
	if connected == nil || connected.ctx.Err() != nil {
		return nil, errors.New("SSH 隧道尚未连接")
	}
	if !allowed {
		return nil, fmt.Errorf("远端地址 %s 不属于当前隧道", remoteAddress)
	}

	// Wildcard listeners are reached on the server's loopback interface.
	switch host {
	case "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	dialContext, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(connected.ctx, cancel)
	defer stop()
	conn, err := connected.client.DialContext(dialContext, "tcp", address(host, port))
	if connected.ctx.Err() != nil {
		if conn != nil {
			conn.Close()
		}
		return nil, errors.New("SSH 隧道已断开，请等待重新连接")
	}
	if err != nil {
		var channelError *ssh.OpenChannelError
		if errors.As(err, &channelError) && channelError.Reason == ssh.Prohibited {
			return nil, fmt.Errorf("服务器拒绝通过 SSH 访问远端端口，请检查 AllowTcpForwarding 和 PermitOpen：%w", err)
		}
		return nil, fmt.Errorf("无法通过 SSH 访问远端端口 %s：%w", remoteAddress, err)
	}
	return conn, nil
}
