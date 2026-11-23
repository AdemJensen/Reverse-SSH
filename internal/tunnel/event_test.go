package tunnel

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestManagerEmitsEventsWithoutPolling(t *testing.T) {
	local, remote, server := listenLoopback(t), listenLoopback(t), listenLoopback(t)
	signer := testSigner(t)
	sshConfig := &ssh.ServerConfig{NoClientAuth: true}
	sshConfig.AddHostKey(signer)
	dropTransport := make(chan struct{}, 1)
	go serveFixtureSSH(server, sshConfig, dropTransport, nil, false)

	events := make(chan Event, 128)
	manager := NewWithEvents(filepath.Join(t.TempDir(), "known_hosts"), func(event Event) {
		events <- event
	})
	t.Cleanup(manager.Stop)
	config := Config{
		SSHHost: "127.0.0.1", SSHPort: server.Addr().(*net.TCPAddr).Port, SSHUser: "fixture",
		AuthMode: AuthPassword, AutoReconnect: true,
		LocalPort: local.Addr().(*net.TCPAddr).Port, RemotePort: remote.Addr().(*net.TCPAddr).Port,
	}
	secrets := Secrets{Password: "event-test-password"}
	start := func(config Config, secrets Secrets) {
		t.Helper()
		if err := manager.Start(config, secrets); err != nil {
			t.Fatal(err)
		}
	}

	start(config, secrets)
	awaitEvent(t, events, "connection_attempt")
	awaitEvent(t, events, "tcp_connected")
	awaitEvent(t, events, "ssh_handshake_started")
	prompt := awaitEvent(t, events, "host_key_required")
	if !prompt.Status || prompt.State != StateHostKey || prompt.Level != levelWarning {
		t.Fatalf("unexpected host-key event: %+v", prompt)
	}
	if err := manager.TrustHost(ssh.FingerprintSHA256(signer.PublicKey())); err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, events, "host_key_trusted")

	withKey := config
	withKey.AuthMode = AuthKey
	withKey.PrivateKeyPath = filepath.Join(t.TempDir(), "private-key-test-marker")
	start(withKey, Secrets{KeyPassphrase: "event-test-passphrase"})
	failed := awaitEvent(t, events, "auth_prepare_failed")
	if !failed.Status || failed.State != StateError || failed.Level != levelError || strings.Contains(failed.Message, "private-key-test-marker") {
		t.Fatalf("unexpected private-key failure event: %+v", failed)
	}
	withoutRetry := config
	withoutRetry.AutoReconnect = false
	start(withoutRetry, secrets)
	denied := awaitEvent(t, events, "remote_bind_failed")
	if !denied.Status || denied.State != StateError || !strings.Contains(denied.Message, "服务器拒绝远端监听请求") {
		t.Fatalf("unexpected remote bind failure event: %+v", denied)
	}
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	start(config, secrets)
	awaitEvent(t, events, "host_key_verified")
	awaitEvent(t, events, "ssh_authenticated")
	awaitEvent(t, events, "remote_bind_requested")
	awaitEvent(t, events, "remote_bound")
	connected := awaitEvent(t, events, "session_connected")
	if !connected.Status || connected.State != StateConnected || connected.Time.IsZero() {
		t.Fatalf("unexpected connected event: %+v", connected)
	}

	dropTransport <- struct{}{}
	lost := awaitEvent(t, events, "remote_listener_lost")
	if lost.Status || !strings.Contains(lost.Message, "EOF") {
		t.Fatalf("unexpected lost listener event: %+v", lost)
	}
	retry := awaitEvent(t, events, "retry_scheduled")
	if !retry.Status || retry.State != StateReconnecting || !strings.Contains(retry.Message, "重试") {
		t.Fatalf("unexpected retry event: %+v", retry)
	}
	awaitEvent(t, events, "connection_attempt")
	awaitEvent(t, events, "session_connected")
	manager.Stop()
	awaitEvent(t, events, "stop_requested")
	stopped := awaitEvent(t, events, "session_stopped")
	if !stopped.Status || stopped.State != StateStopped {
		t.Fatalf("unexpected stopped event: %+v", stopped)
	}
}

func awaitEvent(t *testing.T, events <-chan Event, kind string) Event {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if strings.Contains(event.Message, "event-test-password") || strings.Contains(event.Message, "event-test-passphrase") {
				t.Fatalf("event included SSH credentials: %+v", event)
			}
			if event.Kind == kind {
				return event
			}
		case <-timer.C:
			t.Fatalf("missing tunnel event %s", kind)
		}
	}
}
