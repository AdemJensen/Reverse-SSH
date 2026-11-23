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
	sshAddress := address(config.SSHHost, config.SSHPort)
	m.trace("connection_attempt", levelInfo, "正在尝试连接 SSH 服务器 "+sshAddress)
	hostKeyCheck, err := hostKeyCallback(m.knownHostsPath)
	if err != nil {
		return false, false, failure("host_key_read_failed", "无法读取 SSH 服务器指纹记录", err)
	}
	dialer := net.Dialer{Timeout: connectionTimeout}
	transport, err := dialer.DialContext(ctx, "tcp", sshAddress)
	if err != nil {
		return false, true, failure("tcp_connect_failed", networkFailure("无法连接 SSH 服务器 "+sshAddress, err),
			fmt.Errorf("无法连接 SSH 服务器：%w", err))
	}
	defer transport.Close()
	stopClosing := context.AfterFunc(ctx, func() { transport.Close() })
	defer stopClosing()
	m.trace("tcp_connected", levelInfo, "已建立 TCP 连接 "+transport.RemoteAddr().String())

	authName := authModeName(config.AuthMode)
	auth, closeAuth, err := prepareAuth(ctx, config, secrets)
	if err != nil {
		return false, config.AuthMode == AuthAgent, failure("auth_prepare_failed", "无法准备 SSH 认证（"+authName+"）", err)
	}
	err = transport.SetDeadline(time.Now().Add(connectionTimeout))
	if err != nil {
		closeAuth()
		return false, true, failure("ssh_transport_failed", "无法设置 SSH 连接超时", err)
	}
	m.trace("ssh_handshake_started", levelInfo, "开始 SSH 握手（认证设置："+authName+"）")
	sshConn, channels, requests, err := ssh.NewClientConn(transport, sshAddress, &ssh.ClientConfig{
		User: config.SSHUser,
		Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: func(host string, remote net.Addr, key ssh.PublicKey) error {
			if err := hostKeyCheck(host, remote, key); err != nil {
				return err
			}
			m.trace("host_key_verified", levelInfo, "SSH 服务器指纹校验通过（"+ssh.FingerprintSHA256(key)+"）")
			return nil
		},
	})
	closeAuth()
	if err != nil {
		kind := "ssh_handshake_failed"
		message := networkFailure("SSH 握手或认证失败（认证设置："+authName+"）", err)
		var mismatch *hostKeyMismatch
		if errors.As(err, &mismatch) {
			kind = "host_key_mismatch"
			message = mismatch.Error()
		}
		return false, isTransportError(err), failure(kind, message, fmt.Errorf("SSH 握手或认证失败：%w", err))
	}
	m.trace("ssh_authenticated", levelInfo, "SSH 握手与认证完成")
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
		localAddress := address(forward.LocalHost, forward.LocalPort)
		m.trace("remote_bind_requested", levelInfo, fmt.Sprintf("正在请求远端监听 %s，转发至本机 %s", remoteAddress, localAddress))
		listener, err := client.Listen("tcp", remoteAddress)
		if err != nil {
			transportFailed := isTransportError(err)
			message := "SSH 服务器拒绝远端监听请求 " + remoteAddress + "，请检查转发权限和端口占用情况"
			if transportFailed {
				message = networkFailure("远端监听请求未完成 "+remoteAddress, err)
			}
			return false, config.RetryForever || transportFailed, failure("remote_bind_failed", message,
				fmt.Errorf("远端无法监听 %s，请检查服务器转发权限和端口占用情况：%w", remoteAddress, err))
		}
		remoteAddresses = append(remoteAddresses, remoteAddress)
		m.trace("remote_bound", levelInfo, "服务器已接受远端监听请求 "+remoteAddress)
		workers.Go(func() {
			err := acceptForward(sessionContext, listener, localAddress, ready, &workers)
			message := "远端监听 " + remoteAddress + " 已中断"
			acceptErrors <- failure("remote_listener_lost", networkFailure(message, err), fmt.Errorf("%s：%w", message, err))
		})
	}
	if err = transport.SetDeadline(time.Time{}); err != nil {
		return false, true, failure("ssh_transport_failed", "无法清除 SSH 连接超时", err)
	}

	m.setConnected(sessionContext, client, remoteAddresses)
	close(ready)
	workers.Go(func() { m.keepAlive(sessionContext, ctx, transport, client) })
	return true, true, <-acceptErrors
}

func authModeName(mode string) string {
	switch mode {
	case AuthPassword:
		return "密码"
	case AuthAgent:
		return "SSH Agent"
	default:
		return "私钥文件"
	}
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

func (m *Manager) keepAlive(ctx, runContext context.Context, transport net.Conn, client *ssh.Client) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := probeKeepAlive(transport, client); err != nil {
				if runContext.Err() == nil {
					m.trace("keepalive_failed", levelWarning, networkFailure("SSH 保活探测失败", err))
				}
				transport.Close()
				return
			}
		}
	}
}

func probeKeepAlive(transport net.Conn, client *ssh.Client) error {
	if err := transport.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	// Even a negative reply proves the SSH peer is alive.
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		return err
	}
	return transport.SetDeadline(time.Time{})
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
