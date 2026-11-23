package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestReverseForwardAndHostKeyPin(t *testing.T) {
	local := listenLoopback(t)
	remote := listenLoopback(t)
	sshListener := listenLoopback(t)
	remote.Close()
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	signer := testSigner(t)
	sshConfig := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) != "fixture-password" {
				return nil, ssh.ErrNoAuth
			}
			return nil, nil
		},
	}
	sshConfig.AddHostKey(signer)
	dropTransport := make(chan struct{}, 1)
	go serveFixtureSSH(sshListener, sshConfig, dropTransport, nil, false)
	go serveFixtureLocal(local, "local service received: ")

	manager := New(knownHosts)
	t.Cleanup(manager.Stop)
	config := Config{
		SSHHost: "127.0.0.1", SSHPort: sshListener.Addr().(*net.TCPAddr).Port, SSHUser: "fixture",
		AuthMode:  AuthPassword,
		LocalHost: "127.0.0.1", LocalPort: local.Addr().(*net.TCPAddr).Port,
		RemoteHost: "127.0.0.1", RemotePort: remote.Addr().(*net.TCPAddr).Port,
		AutoReconnect: true,
	}
	secrets := Secrets{Password: "fixture-password"}
	if err := manager.Start(config, secrets); err != nil {
		t.Fatal(err)
	}
	status := awaitState(t, manager, StateHostKey)
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	if status.HostKey == nil || status.HostKey.Fingerprint != fingerprint {
		t.Fatalf("unexpected fingerprint prompt: %+v", status)
	}
	if err := manager.TrustHost("SHA256:wrong-fingerprint"); err == nil {
		t.Fatal("accepted a fingerprint other than the one shown in the prompt")
	}
	if err := manager.TrustHost(fingerprint); err != nil {
		t.Fatal(err)
	}
	checkKey, err := hostKeyCallback(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkKey(status.HostKey.Host, sshListener.Addr(), testSigner(t).PublicKey()); err == nil {
		t.Fatal("accepted a changed key for the pinned host")
	}
	if err := manager.Start(config, secrets); err != nil {
		t.Fatal(err)
	}
	awaitState(t, manager, StateConnected)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := manager.DialRemote(ctx, "127.0.0.1", config.RemotePort); err == nil {
		conn.Close()
		t.Fatal("opened a direct channel despite the SSH server denying it")
	} else if !strings.Contains(err.Error(), "AllowTcpForwarding") {
		t.Fatalf("expected a forwarding permission error, got %v", err)
	}

	assertForwardedResponse(t, remote.Addr().String(), "local service received: payload")
	dropTransport <- struct{}{}
	awaitState(t, manager, StateReconnecting)
	awaitState(t, manager, StateConnected)
	manager.Stop()
	awaitState(t, manager, StateStopped)
}

func TestReverseMultipleForwardsRollbackAndReconnect(t *testing.T) {
	firstLocal, secondLocal := listenLoopback(t), listenLoopback(t)
	firstRemote, secondRemote := listenLoopback(t), listenLoopback(t)
	firstRemote.Close()
	go serveFixtureLocal(firstLocal, "first: ")
	go serveFixtureLocal(secondLocal, "second: ")

	sshListener := listenLoopback(t)
	signer := testSigner(t)
	sshConfig := &ssh.ServerConfig{NoClientAuth: true}
	sshConfig.AddHostKey(signer)
	dropTransport := make(chan struct{}, 1)
	released := make(chan struct{}, 3)
	go serveFixtureSSH(sshListener, sshConfig, dropTransport, released, true)
	manager := New(filepath.Join(t.TempDir(), "known_hosts"))
	t.Cleanup(manager.Stop)
	config := Config{
		SSHHost: "127.0.0.1", SSHPort: sshListener.Addr().(*net.TCPAddr).Port, SSHUser: "fixture",
		AuthMode: AuthPassword, AutoReconnect: true,
		Forwards: []Forward{
			{LocalPort: firstLocal.Addr().(*net.TCPAddr).Port, RemotePort: firstRemote.Addr().(*net.TCPAddr).Port},
			{LocalPort: secondLocal.Addr().(*net.TCPAddr).Port, RemotePort: secondRemote.Addr().(*net.TCPAddr).Port},
		},
	}
	secrets := Secrets{Password: "fixture-password"}
	start := func() {
		t.Helper()
		if err := manager.Start(config, secrets); err != nil {
			t.Fatal(err)
		}
	}
	start()
	awaitState(t, manager, StateHostKey)
	if err := manager.TrustHost(ssh.FingerprintSHA256(signer.PublicKey())); err != nil {
		t.Fatal(err)
	}

	start()
	failed := awaitState(t, manager, StateError)
	if !strings.Contains(failed.Message, secondRemote.Addr().String()) || len(failed.RemoteAddresses) != 0 {
		t.Fatalf("unexpected partial bind result: %+v", failed)
	}
	awaitForwardCleanup(t, released)

	secondRemote.Close()
	start()
	checkRoutes := func() {
		t.Helper()
		status := awaitState(t, manager, StateConnected)
		want := []string{firstRemote.Addr().String(), secondRemote.Addr().String()}
		if !slices.Equal(status.RemoteAddresses, want) || status.RemoteAddress != want[0] {
			t.Fatalf("unexpected remote addresses: %+v", status)
		}
		assertForwardedResponse(t, want[0], "first: payload")
		assertForwardedResponse(t, want[1], "second: payload")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := manager.DialRemote(ctx, "127.0.0.1", config.Forwards[0].RemotePort)
		if err != nil {
			t.Fatal(err)
		}
		assertConnectionResponse(t, conn, "first: payload")
		if conn, err := manager.DialRemote(ctx, "localhost", config.Forwards[0].RemotePort); err == nil {
			conn.Close()
			t.Fatal("dialed an endpoint that was not configured for this SSH connection")
		} else if !strings.Contains(err.Error(), "不属于当前隧道") {
			t.Fatalf("expected an unconfigured address error, got %v", err)
		}
	}
	checkRoutes()
	dropTransport <- struct{}{}
	awaitState(t, manager, StateReconnecting)
	if conn, err := manager.DialRemote(context.Background(), "127.0.0.1", config.Forwards[0].RemotePort); err == nil {
		conn.Close()
		t.Fatal("dialed while the SSH connection was reconnecting")
	}
	awaitForwardCleanup(t, released)
	checkRoutes()

	manager.Stop()
	awaitState(t, manager, StateStopped)
	awaitForwardCleanup(t, released)
}

func TestRetryForeverWaitsForRemoteListener(t *testing.T) {
	local, occupied, sshListener := listenLoopback(t), listenLoopback(t), listenLoopback(t)
	go serveFixtureLocal(local, "recovered: ")
	signer := testSigner(t)
	secrets := Secrets{Password: "fixture-password"}
	sshConfig := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) != secrets.Password {
				return nil, ssh.ErrNoAuth
			}
			return nil, nil
		},
	}
	sshConfig.AddHostKey(signer)
	dropTransport := make(chan struct{}, 1)
	go serveFixtureSSH(sshListener, sshConfig, dropTransport, nil, false)
	manager := New(filepath.Join(t.TempDir(), "known_hosts"))
	t.Cleanup(manager.Stop)
	config := Config{
		SSHHost: "127.0.0.1", SSHPort: sshListener.Addr().(*net.TCPAddr).Port, SSHUser: "fixture",
		AuthMode: AuthPassword, LocalPort: local.Addr().(*net.TCPAddr).Port, RemotePort: occupied.Addr().(*net.TCPAddr).Port,
		AutoReconnect: true, RetryForever: true,
	}
	start := func(secrets Secrets) {
		t.Helper()
		if err := manager.Start(config, secrets); err != nil {
			t.Fatal(err)
		}
	}
	start(secrets)
	awaitState(t, manager, StateHostKey)
	if err := manager.TrustHost(ssh.FingerprintSHA256(signer.PublicKey())); err != nil {
		t.Fatal(err)
	}

	config.AutoReconnect = false
	start(secrets)
	awaitStatus(t, manager, StateError, "远端无法监听")
	config.AutoReconnect = true
	start(Secrets{Password: "rejected-fixture-password"})
	awaitStatus(t, manager, StateError, "认证失败")

	start(secrets)
	awaitStatus(t, manager, StateReconnecting, "远端无法监听")
	stopped := make(chan struct{})
	go func() {
		manager.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not interrupt retry backoff")
	}
	awaitState(t, manager, StateStopped)

	start(secrets)
	awaitStatus(t, manager, StateReconnecting, "第 2 次重试")
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	awaitState(t, manager, StateConnected)
	assertForwardedResponse(t, occupied.Addr().String(), "recovered: payload")
	dropTransport <- struct{}{}
	awaitStatus(t, manager, StateReconnecting, "第 1 次重试")
}

func awaitForwardCleanup(t *testing.T, released <-chan struct{}) {
	t.Helper()
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("SSH connection did not release its remote listeners")
	}
}

func assertForwardedResponse(t *testing.T, remote, expected string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", remote, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertConnectionResponse(t, conn, expected)
}

func assertConnectionResponse(t *testing.T, conn net.Conn, expected string) {
	t.Helper()
	defer conn.Close()
	timer := time.AfterFunc(4*time.Second, func() { conn.Close() })
	defer timer.Stop()
	if _, err := conn.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != expected {
		t.Fatalf("incorrect forwarded response: %q", reply)
	}
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func awaitState(t *testing.T, manager *Manager, expected string) Status {
	t.Helper()
	return awaitStatus(t, manager, expected, "")
}

func awaitStatus(t *testing.T, manager *Manager, expected, message string) Status {
	t.Helper()
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		status := manager.Snapshot()
		if status.State == expected && strings.Contains(status.Message, message) {
			return status
		}
		if status.State == StateError {
			t.Fatalf("expected %s with %q but got error: %s", expected, message, status.Message)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %s with %q but got %+v", expected, message, manager.Snapshot())
	return Status{}
}

func serveFixtureLocal(listener net.Listener, prefix string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(4 * time.Second))
			input, err := io.ReadAll(conn)
			if err == nil {
				conn.Write(append([]byte(prefix), input...))
			}
		}()
	}
}

func serveFixtureSSH(listener net.Listener, config *ssh.ServerConfig, dropTransport <-chan struct{}, released chan<- struct{}, allowDirect bool) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handleFixtureSSH(conn, config, dropTransport, released, allowDirect)
	}
}

func handleFixtureSSH(conn net.Conn, config *ssh.ServerConfig, dropTransport <-chan struct{}, released chan<- struct{}, allowDirect bool) {
	defer conn.Close()
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	done := make(chan struct{})
	defer close(done)
	var forwards []net.Listener
	defer func() {
		for _, listener := range forwards {
			listener.Close()
		}
		if released != nil && len(forwards) != 0 {
			released <- struct{}{}
		}
	}()
	go func() {
		select {
		case <-dropTransport:
			conn.Close()
		case <-done:
		}
	}()
	var activeForwards sync.Map
	go serveFixtureChannels(channels, &activeForwards, allowDirect)
	for request := range requests {
		var bind struct {
			Host string
			Port uint32
		}
		if request.Type != "tcpip-forward" || ssh.Unmarshal(request.Payload, &bind) != nil ||
			bind.Host != "127.0.0.1" || bind.Port == 0 || bind.Port > 65535 {
			request.Reply(false, nil)
			continue
		}
		remote, err := net.Listen("tcp", address(bind.Host, int(bind.Port)))
		if err != nil {
			request.Reply(false, nil)
			continue
		}
		forwards = append(forwards, remote)
		activeForwards.Store(remote.Addr().String(), struct{}{})
		if request.Reply(true, nil) == nil {
			go serveFixtureRemote(server, remote)
		}
	}
}

func serveFixtureChannels(channels <-chan ssh.NewChannel, forwards *sync.Map, allowDirect bool) {
	for channel := range channels {
		if channel.ChannelType() != "direct-tcpip" {
			channel.Reject(ssh.UnknownChannelType, "unsupported fixture channel")
			continue
		}
		var endpoint struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if !allowDirect || ssh.Unmarshal(channel.ExtraData(), &endpoint) != nil {
			channel.Reject(ssh.Prohibited, "direct channel denied by fixture")
			continue
		}
		target := address(endpoint.Host, int(endpoint.Port))
		if _, allowed := forwards.Load(target); !allowed {
			channel.Reject(ssh.Prohibited, "target is not a fixture listener")
			continue
		}
		go openFixtureDirectChannel(channel, target)
	}
}

func openFixtureDirectChannel(incoming ssh.NewChannel, target string) {
	conn, err := net.DialTimeout("tcp", target, time.Second)
	if err != nil {
		incoming.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	defer conn.Close()
	channel, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	relayFixtureChannel(conn, channel, requests)
}

func serveFixtureRemote(server *ssh.ServerConn, remote net.Listener) {
	for {
		conn, err := remote.Accept()
		if err != nil {
			return
		}
		go forwardFixtureConnection(server, conn, remote.Addr().(*net.TCPAddr).Port)
	}
}

func forwardFixtureConnection(server *ssh.ServerConn, conn net.Conn, remotePort int) {
	defer conn.Close()
	peer := conn.RemoteAddr().(*net.TCPAddr)
	channel, requests, err := server.OpenChannel("forwarded-tcpip", ssh.Marshal(struct {
		BindHost string
		BindPort uint32
		PeerHost string
		PeerPort uint32
	}{"127.0.0.1", uint32(remotePort), peer.IP.String(), uint32(peer.Port)}))
	if err != nil {
		return
	}
	relayFixtureChannel(conn, channel, requests)
}

func relayFixtureChannel(conn net.Conn, channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(channel, conn)
		channel.CloseWrite()
	}()
	io.Copy(conn, channel)
	conn.(*net.TCPConn).CloseWrite()
	<-done
}
