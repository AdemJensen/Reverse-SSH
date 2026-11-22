package tunnel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

const (
	StateStopped      = "stopped"
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateReconnecting = "reconnecting"
	StateHostKey      = "hostkey"
	StateError        = "error"
)

type Status struct {
	State           string
	Message         string
	HostKey         *HostKey
	RemoteAddress   string
	RemoteAddresses []string
}

type HostKey struct {
	Host        string
	Type        string
	Fingerprint string
}

type Manager struct {
	knownHostsPath string
	mu             sync.Mutex
	status         Status
	active         *session
	connected      *connectedSession
	pending        *hostKeyPrompt
}

type session struct {
	context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func New(knownHostsPath string) *Manager {
	return &Manager{
		knownHostsPath: knownHostsPath,
		status:         Status{State: StateStopped, Message: "隧道已断开"},
	}
}

func (m *Manager) Snapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status
	status.RemoteAddresses = slices.Clone(status.RemoteAddresses)
	if m.pending != nil {
		hostKey := m.pending.hostKey
		status.HostKey = &hostKey
	}
	return status
}

func (m *Manager) Start(config Config, secrets Secrets) error {
	config = config.normalized()
	if err := config.validate(); err != nil {
		return err
	}
	if config.AuthMode == AuthPassword && secrets.Password == "" {
		return errors.New("请输入 SSH 密码")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		return errors.New("已有一条隧道正在连接或运行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &session{Context: ctx, cancel: cancel, done: make(chan struct{})}
	m.active = run
	m.pending = nil
	m.status = Status{State: StateConnecting, Message: "正在连接 SSH 服务器"}
	go m.run(run, config, secrets)
	return nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	run := m.active
	if run == nil {
		m.status = Status{State: StateStopped, Message: "隧道已断开"}
		m.pending = nil
	} else {
		run.cancel()
	}
	m.mu.Unlock()
	if run != nil {
		<-run.done
	}
}

func (m *Manager) TrustHost(fingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.hostKey.Fingerprint != fingerprint {
		return errors.New("服务器指纹已变化，请重新连接并核对")
	}
	if err := m.pending.save(m.knownHostsPath); err != nil {
		return fmt.Errorf("无法保存服务器指纹：%w", err)
	}
	m.pending = nil
	m.status = Status{State: StateStopped, Message: "服务器指纹已保存，可以连接"}
	return nil
}

func (m *Manager) run(run *session, config Config, secrets Secrets) {
	defer close(run.done)
	defer run.cancel()
	const firstRetryDelay = 2 * time.Second
	retryDelay := firstRetryDelay
	final := Status{State: StateStopped, Message: "隧道已断开"}
	var prompt *hostKeyPrompt
	for run.Err() == nil {
		connected, retryable, err := m.connectAndForward(run, config, secrets)
		if run.Err() != nil {
			break
		}
		if errors.As(err, &prompt) {
			final = Status{State: StateHostKey, Message: "首次连接，请核对 SSH 服务器指纹"}
			break
		}
		if !config.AutoReconnect || !retryable {
			final = Status{State: StateError, Message: err.Error()}
			break
		}
		if connected {
			retryDelay = firstRetryDelay
		}
		m.update(Status{
			State:   StateReconnecting,
			Message: fmt.Sprintf("%s。%d 秒后重试", err, int(retryDelay/time.Second)),
		})
		select {
		case <-run.Done():
		case <-time.After(retryDelay):
			retryDelay = min(retryDelay*2, 30*time.Second)
		}
	}

	m.mu.Lock()
	if run.Err() != nil {
		final = Status{State: StateStopped, Message: "隧道已断开"}
		prompt = nil
	}
	m.active = nil
	m.connected = nil
	m.pending = prompt
	m.status = final
	m.mu.Unlock()
}

func (m *Manager) update(status Status) {
	m.mu.Lock()
	m.status = status
	m.connected = nil
	m.mu.Unlock()
}
