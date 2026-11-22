package tunnel

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	AuthKey      = "key"
	AuthPassword = "password"
	AuthAgent    = "agent"
)

type Config struct {
	SSHHost        string
	SSHPort        int
	SSHUser        string
	AuthMode       string
	PrivateKeyPath string
	LocalHost      string
	LocalPort      int
	RemoteHost     string
	RemotePort     int
	Forwards       []Forward
	AutoReconnect  bool
}

type Forward struct {
	LocalHost  string
	LocalPort  int
	RemoteHost string
	RemotePort int
}

type Secrets struct {
	Password      string
	KeyPassphrase string
}

func (c Config) Validate() error {
	return c.normalized().validate()
}

// ValidateForwards checks the configured forwarding addresses without checking SSH credentials.
func (c Config) ValidateForwards() error {
	return c.normalized().validateForwards()
}

func (c Config) normalized() Config {
	c.SSHHost = normalizeHost(c.SSHHost)
	c.SSHUser = strings.TrimSpace(c.SSHUser)
	forwards := c.Forwards
	if len(forwards) == 0 {
		forwards = []Forward{{
			LocalHost: c.LocalHost, LocalPort: c.LocalPort,
			RemoteHost: c.RemoteHost, RemotePort: c.RemotePort,
		}}
	}
	c.Forwards = make([]Forward, len(forwards))
	for index, forward := range forwards {
		forward.LocalHost = normalizeForwardHost(forward.LocalHost)
		forward.RemoteHost = normalizeForwardHost(forward.RemoteHost)
		c.Forwards[index] = forward
	}
	return c
}

func normalizeForwardHost(host string) string {
	if normalized := normalizeHost(host); normalized != "" {
		return normalized
	}
	return "127.0.0.1"
}

// NormalizeForwardEndpoint validates an endpoint using the forwarding address rules.
func NormalizeForwardEndpoint(host string, port int) (string, error) {
	host = normalizeForwardHost(host)
	return host, validateEndpoint("目标", host, port)
}

func (c Config) validate() error {
	if err := validateEndpoint("SSH 服务器", c.SSHHost, c.SSHPort); err != nil {
		return err
	}
	if err := c.validateForwards(); err != nil {
		return err
	}
	if c.SSHUser == "" || strings.ContainsAny(c.SSHUser, "\r\n\x00") {
		return errors.New("请输入 SSH 用户名")
	}
	switch c.AuthMode {
	case AuthKey:
		if strings.TrimSpace(c.PrivateKeyPath) == "" {
			return errors.New("请选择 SSH 私钥文件")
		}
	case AuthPassword, AuthAgent:
	default:
		return errors.New("请选择 SSH 认证方式")
	}
	return nil
}

func (c Config) validateForwards() error {
	seen := make(map[string]struct{}, len(c.Forwards))
	for index, forward := range c.Forwards {
		if err := validateEndpoint("本地服务", forward.LocalHost, forward.LocalPort); err != nil {
			return fmt.Errorf("第 %d 条转发：%w", index+1, err)
		}
		if err := validateEndpoint("远端监听", forward.RemoteHost, forward.RemotePort); err != nil {
			return fmt.Errorf("第 %d 条转发：%w", index+1, err)
		}
		remote := address(forward.RemoteHost, forward.RemotePort)
		if _, duplicate := seen[remote]; duplicate {
			return fmt.Errorf("第 %d 条转发的远端监听地址 %s 重复", index+1, remote)
		}
		seen[remote] = struct{}{}
	}
	return nil
}

func validateEndpoint(label, host string, port int) error {
	if !validHost(host) {
		return fmt.Errorf("%s地址必须是主机名或 IP 地址", label)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s端口必须在 1–65535 之间", label)
	}
	return nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		if ip, err := netip.ParseAddr(host[1 : len(host)-1]); err == nil && ip.Is6() {
			return ip.String()
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String()
	}
	return strings.ToLower(host)
}

func validHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return validHostname(ip.Zone())
		}
		return true
	}
	return validHostname(host)
}

func validHostname(host string) bool {
	name := strings.TrimSuffix(host, ".")
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_", character) {
				return false
			}
		}
	}
	return true
}

func address(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
