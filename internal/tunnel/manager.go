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
	sink           func(Event)
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
	return NewWithEvents(knownHostsPath, nil)
}

// NewWithEvents creates a tunnel manager with a synchronous, serialized event sink.
// The sink must return promptly and must not call back into the manager or its owner.
func NewWithEvents(knownHostsPath string, sink func(Event)) *Manager {
	return &Manager{
		knownHostsPath: knownHostsPath,
		sink:           sink,
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
	status := Status{State: StateConnecting, Message: "正在连接 SSH 服务器"}
	m.setStatusLocked(status, "session_started", levelInfo, status.Message)
	go m.run(run, config, secrets)
	return nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	run := m.active
	if run == nil {
		status := Status{State: StateStopped, Message: "隧道已断开"}
		if m.status.State != StateStopped {
			m.emitLocked("stop_requested", levelInfo, "已请求停止 SSH 隧道", false)
			m.setStatusLocked(status, "session_stopped", levelInfo, status.Message)
		} else {
			m.status = status
		}
		m.pending = nil
	} else if run.Err() == nil {
		m.emitLocked("stop_requested", levelInfo, "已请求停止 SSH 隧道", false)
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
		m.emitLocked("host_key_save_failed", levelError, "无法保存 SSH 服务器指纹", false)
		return fmt.Errorf("无法保存服务器指纹：%w", err)
	}
	fingerprint = m.pending.hostKey.Fingerprint
	m.pending = nil
	status := Status{State: StateStopped, Message: "服务器指纹已保存，可以连接"}
	m.setStatusLocked(status, "host_key_trusted", levelInfo, "已信任 SSH 服务器指纹 "+fingerprint)
	return nil
}

func (m *Manager) run(run *session, config Config, secrets Secrets) {
	defer close(run.done)
	defer run.cancel()
	const firstRetryDelay = 2 * time.Second
	retryDelay := firstRetryDelay
	retryCount := 0
	final := Status{State: StateStopped, Message: "隧道已断开"}
	finalKind, finalLevel, finalMessage := "session_stopped", levelInfo, final.Message
	var prompt *hostKeyPrompt
	for run.Err() == nil {
		connected, retryable, err := m.connectAndForward(run, config, secrets)
		if run.Err() != nil {
			break
		}
		if errors.As(err, &prompt) {
			final = Status{State: StateHostKey, Message: "首次连接，请核对 SSH 服务器指纹"}
			finalKind, finalLevel = "host_key_required", levelWarning
			finalMessage = final.Message + "（" + prompt.hostKey.Fingerprint + "）"
			break
		}
		kind, message := describeFailure(err)
		if !config.AutoReconnect || !retryable {
			final = Status{State: StateError, Message: err.Error()}
			finalKind, finalLevel, finalMessage = kind, levelError, message
			break
		}
		if connected {
			retryDelay = firstRetryDelay
			retryCount = 0
		}
		retry := "重试"
		if config.RetryForever {
			retryCount++
			retry = fmt.Sprintf("第 %d 次重试", retryCount)
		}
		schedule := fmt.Sprintf("%d 秒后%s", int(retryDelay/time.Second), retry)
		m.mu.Lock()
		if run.Err() == nil {
			m.emitLocked(kind, levelWarning, message, false)
			m.connected = nil
			m.setStatusLocked(Status{State: StateReconnecting, Message: err.Error() + "。" + schedule},
				"retry_scheduled", levelWarning, message+"。"+schedule)
		}
		m.mu.Unlock()
		select {
		case <-run.Done():
		case <-time.After(retryDelay):
			retryDelay = min(retryDelay*2, 30*time.Second)
		}
	}

	m.mu.Lock()
	if run.Err() != nil {
		final = Status{State: StateStopped, Message: "隧道已断开"}
		finalKind, finalLevel, finalMessage = "session_stopped", levelInfo, final.Message
		prompt = nil
	}
	m.active = nil
	m.connected = nil
	m.pending = prompt
	m.setStatusLocked(final, finalKind, finalLevel, finalMessage)
	m.mu.Unlock()
}
