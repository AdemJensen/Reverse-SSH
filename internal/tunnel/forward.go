package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const connectionTimeout = 15 * time.Second

func (m *Manager) connectAndForward(ctx context.Context, config Config, secrets Secrets) (connected, retryable bool, err error) {
	hostKeyCheck, err := hostKeyCallback(m.knownHostsPath)
	if err != nil {
		return false, false, err
	}
	sshAddress := address(config.SSHHost, config.SSHPort)
	dialer := net.Dialer{Timeout: connectionTimeout}
	transport, err := dialer.DialContext(ctx, "tcp", sshAddress)
	if err != nil {
		return false, true, fmt.Errorf("无法连接 SSH 服务器：%w", err)
	}
	defer transport.Close()
	stopClosing := context.AfterFunc(ctx, func() { transport.Close() })
	defer stopClosing()

	auth, closeAuth, err := prepareAuth(ctx, config, secrets)
	if err != nil {
		return false, config.AuthMode == AuthAgent, err
	}
	err = transport.SetDeadline(time.Now().Add(connectionTimeout))
	if err != nil {
		closeAuth()
		return false, true, err
	}
	sshConn, channels, requests, err := ssh.NewClientConn(transport, sshAddress, &ssh.ClientConfig{
		User:            config.SSHUser,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: hostKeyCheck,
	})
	closeAuth()
	if err != nil {
		return false, isTransportError(err), fmt.Errorf("SSH 握手或认证失败：%w", err)
	}
	client := ssh.NewClient(sshConn, channels, requests)
	sessionContext, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		transport.Close()
		workers.Wait()
	}()

	// Service listeners as soon as they are registered so incoming channels
	// cannot block the SSH connection while the other requests are pending.
	ready := make(chan struct{})
	acceptErrors := make(chan error, len(config.Forwards))
	remoteAddresses := make([]string, 0, len(config.Forwards))
	for _, forward := range config.Forwards {
		remoteAddress := address(forward.RemoteHost, forward.RemotePort)
		listener, err := client.Listen("tcp", remoteAddress)
		if err != nil {
			return false, isTransportError(err), fmt.Errorf("远端无法监听 %s，请检查服务器转发权限和端口占用情况：%w", remoteAddress, err)
		}
		remoteAddresses = append(remoteAddresses, remoteAddress)
		localAddress := address(forward.LocalHost, forward.LocalPort)
		workers.Go(func() {
			err := acceptForward(sessionContext, listener, localAddress, ready, &workers)
			acceptErrors <- fmt.Errorf("远端监听 %s 已中断：%w", remoteAddress, err)
		})
	}
	if err = transport.SetDeadline(time.Time{}); err != nil {
		return false, true, err
	}

	m.setConnected(sessionContext, client, remoteAddresses)
	close(ready)
	workers.Go(func() { keepAlive(sessionContext, transport, client) })
	return true, true, <-acceptErrors
}

func acceptForward(ctx context.Context, listener net.Listener, localAddress string, ready <-chan struct{}, workers *sync.WaitGroup) error {
	for {
		remote, err := listener.Accept()
		if err != nil {
			return err
		}
		workers.Go(func() {
			select {
			case <-ready:
				forwardConnection(ctx, remote, localAddress)
			case <-ctx.Done():
				remote.Close()
			}
		})
	}
}

func isTransportError(err error) bool {
	var networkError net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &networkError)
}

func keepAlive(ctx context.Context, transport net.Conn, client *ssh.Client) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := transport.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				transport.Close()
				return
			}
			// Even a negative reply proves the SSH peer is alive.
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				transport.Close()
				return
			}
			if err := transport.SetDeadline(time.Time{}); err != nil {
				transport.Close()
				return
			}
		}
	}
}

func forwardConnection(ctx context.Context, remote net.Conn, localAddress string) {
	defer remote.Close()
	dialer := net.Dialer{Timeout: 5 * time.Second}
	local, err := dialer.DialContext(ctx, "tcp", localAddress)
	if err != nil {
		return
	}
	defer local.Close()
	stopClosing := context.AfterFunc(ctx, func() { local.Close() })
	defer stopClosing()

	results := make(chan error, 2)
	go copyStream(results, local, remote)
	go copyStream(results, remote, local)
	if err := <-results; err != nil {
		local.Close()
		remote.Close()
	}
	<-results
}

func copyStream(results chan<- error, destination, source net.Conn) {
	_, err := io.Copy(destination, source)
	if err == nil {
		if writer, ok := destination.(interface{ CloseWrite() error }); ok {
			err = writer.CloseWrite()
		}
	}
	results <- err
}
