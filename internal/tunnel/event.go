package tunnel

import (
	"errors"
	"io"
	"net"
	"syscall"
	"time"
)

// Event describes a tunnel lifecycle event. Status marks a user-visible state change.
// Messages are intended for diagnostic logs and omit credentials and file paths.
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Level   string    `json:"level"`
	State   string    `json:"state"`
	Message string    `json:"message"`
	Status  bool      `json:"status"`
}

const (
	levelInfo    = "info"
	levelWarning = "warning"
	levelError   = "error"
)

func (m *Manager) trace(kind, level, message string) {
	if m.sink == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitLocked(kind, level, message, false)
}

func (m *Manager) setStatusLocked(status Status, kind, level, message string) {
	m.status = status
	m.emitLocked(kind, level, message, true)
}

func (m *Manager) emitLocked(kind, level, message string, status bool) {
	if m.sink != nil {
		m.sink(Event{
			Time: time.Now(), Kind: kind, Level: level, State: m.status.State,
			Message: message, Status: status,
		})
	}
}

// A failure keeps the original error for the live connection status and provides
// a controlled summary for the persistent log.
type attemptFailure struct {
	cause   error
	kind    string
	message string
}

func (e *attemptFailure) Error() string { return e.cause.Error() }
func (e *attemptFailure) Unwrap() error { return e.cause }

func failure(kind, message string, cause error) *attemptFailure {
	return &attemptFailure{cause: cause, kind: kind, message: message}
}

func describeFailure(err error) (kind, message string) {
	var report *attemptFailure
	if errors.As(err, &report) {
		return report.kind, report.message
	}
	return "connection_failed", "SSH 隧道连接失败"
}

func networkFailure(message string, err error) string {
	var dnsError *net.DNSError
	var networkError net.Error
	var detail string
	switch {
	case errors.As(err, &dnsError):
		detail = "域名解析失败"
		if dnsError.Timeout() {
			detail = "域名解析超时"
		}
	case errors.As(err, &networkError) && networkError.Timeout():
		detail = "连接超时"
	case errors.Is(err, syscall.ECONNREFUSED):
		detail = "连接被拒绝"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		detail = "网络不可达"
	case errors.Is(err, syscall.ECONNRESET):
		detail = "连接被重置"
	case errors.Is(err, io.ErrUnexpectedEOF):
		detail = "连接提前结束，unexpected EOF"
	case errors.Is(err, io.EOF):
		detail = "EOF"
	case errors.Is(err, net.ErrClosed):
		detail = "连接已关闭"
	}
	if detail != "" {
		return message + "（" + detail + "）"
	}
	return message
}
