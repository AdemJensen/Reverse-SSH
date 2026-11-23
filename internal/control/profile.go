package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"reversessh/internal/tunnel"
)

const (
	profileVersion  = 2
	maxForwards     = 64
	maxProfileBytes = 128 << 10
)

type profile struct {
	Host                     string    `json:"host"`
	SSHPort                  int       `json:"sshPort"`
	User                     string    `json:"user"`
	Auth                     string    `json:"auth"`
	KeyPath                  string    `json:"keyPath"`
	Reconnect                bool      `json:"reconnect"`
	RetryForever             bool      `json:"retryForever,omitempty"`
	AutoConnect              bool      `json:"autoConnect"`
	RememberPassword         bool      `json:"rememberPassword"`
	SavedPassword            string    `json:"-"`
	SavedPasswordUnavailable bool      `json:"-"`
	Forwards                 []forward `json:"forwards"`
}

type forward struct {
	RemoteBind string `json:"remoteBind"`
	RemotePort int    `json:"remotePort"`
	LocalHost  string `json:"localHost"`
	LocalPort  int    `json:"localPort"`
}

type profileFile struct {
	Version int `json:"version"`
	profile
	// Override the API field so portable files keep the existing schema when disabled.
	RememberPassword  bool            `json:"rememberPassword,omitempty"`
	EncryptedPassword json.RawMessage `json:"encryptedPassword,omitempty"`
	LegacyPassword    json.RawMessage `json:"password,omitempty"`
	LegacyRemoteBind  *string         `json:"remoteBind,omitempty"`
	LegacyRemotePort  *int            `json:"remotePort,omitempty"`
	LegacyLocalHost   *string         `json:"localHost,omitempty"`
	LegacyLocalPort   *int            `json:"localPort,omitempty"`
}

func defaultProfile() profile {
	return profile{
		SSHPort: 22, Auth: tunnel.AuthKey, Reconnect: true,
		Forwards: []forward{{RemoteBind: "127.0.0.1", RemotePort: 8080, LocalHost: "127.0.0.1", LocalPort: 3000}},
	}
}

func loadProfile(path string) (profile, error) {
	p, err := readProfile(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return defaultProfile(), nil
	}
	return p, err
}

func readProfile(path string, importing bool) (profile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return profile{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxProfileBytes {
		return profile{}, errors.New("请选择不超过 128 KB 的 JSON 配置文件。")
	}
	file, err := os.Open(path)
	if err != nil {
		return profile{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxProfileBytes+1))
	if err != nil {
		return profile{}, err
	}
	if len(data) > maxProfileBytes {
		return profile{}, errors.New("配置文件超过 128 KB。")
	}
	defaults := defaultProfile()
	legacy := defaults.Forwards[0]
	defaults.Forwards = nil
	decoded := profileFile{profile: defaults}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if importing {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(&decoded); err != nil {
		return profile{}, fmt.Errorf("无法解析 JSON 配置：%w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return profile{}, errors.New("配置文件包含多余内容。")
	}
	if decoded.Version < 0 || decoded.Version > profileVersion {
		return profile{}, fmt.Errorf("不支持配置文件版本 %d。", decoded.Version)
	}
	if decoded.Forwards == nil && decoded.Version < profileVersion {
		if decoded.LegacyRemoteBind == nil && decoded.LegacyRemotePort == nil &&
			decoded.LegacyLocalHost == nil && decoded.LegacyLocalPort == nil {
			return profile{}, errors.New("配置文件缺少端口转发。")
		}
		if decoded.LegacyRemoteBind != nil {
			legacy.RemoteBind = *decoded.LegacyRemoteBind
		}
		if decoded.LegacyRemotePort != nil {
			legacy.RemotePort = *decoded.LegacyRemotePort
		}
		if decoded.LegacyLocalHost != nil {
			legacy.LocalHost = *decoded.LegacyLocalHost
		}
		if decoded.LegacyLocalPort != nil {
			legacy.LocalPort = *decoded.LegacyLocalPort
		}
		decoded.Forwards = []forward{legacy}
	}
	if len(decoded.Forwards) == 0 || len(decoded.Forwards) > maxForwards {
		return profile{}, fmt.Errorf("配置须包含 1–%d 条端口转发。", maxForwards)
	}
	if importing && len(decoded.LegacyPassword) != 0 {
		return profile{}, errors.New("导入的 SSH 密码须为本应用导出的加密格式。")
	}
	hasCiphertext := len(decoded.EncryptedPassword) != 0
	if !importing || hasCiphertext {
		decoded.profile.RememberPassword = decoded.RememberPassword
	}
	decoded.normalize()
	if !importing || hasCiphertext {
		decoded.restorePassword()
	}
	if importing {
		if hasCiphertext && decoded.SavedPassword == "" {
			return profile{}, errors.New("无法导入加密的 SSH 密码，请重新导出完整配置。")
		}
		if err := decoded.validateStorage(); err != nil {
			return profile{}, err
		}
	}
	return decoded.profile, nil
}

func (f *profileFile) restorePassword() {
	if f.Auth != tunnel.AuthPassword {
		return
	}
	if f.profile.RememberPassword {
		if len(f.LegacyPassword) == 0 && len(f.EncryptedPassword) != 0 {
			secret, err := decryptProfilePassword(f.profile, f.EncryptedPassword)
			if err == nil {
				f.SavedPassword = secret
				return
			}
		}
		f.SavedPasswordUnavailable = f.AutoConnect || len(f.EncryptedPassword) != 0 || len(f.LegacyPassword) != 0
	}
	f.AutoConnect = false
}

func (p *profile) normalize() {
	p.Host = strings.TrimSpace(p.Host)
	p.User = strings.TrimSpace(p.User)
	for index, route := range p.Forwards {
		p.Forwards[index].RemoteBind = strings.TrimSpace(route.RemoteBind)
		p.Forwards[index].LocalHost = strings.TrimSpace(route.LocalHost)
	}
	if p.Auth != tunnel.AuthKey {
		p.KeyPath = ""
	}
	p.normalizePassword()
}

func (p *profile) normalizePassword() {
	p.RememberPassword = p.Auth == tunnel.AuthPassword && p.RememberPassword
	if !p.RememberPassword {
		p.SavedPassword = ""
		p.SavedPasswordUnavailable = false
	}
}

func (p profile) config() tunnel.Config {
	config := tunnel.Config{
		SSHHost: p.Host, SSHPort: p.SSHPort, SSHUser: p.User,
		AuthMode: p.Auth, PrivateKeyPath: p.KeyPath,
		AutoReconnect: p.Reconnect, RetryForever: p.RetryForever,
		Forwards: make([]tunnel.Forward, len(p.Forwards)),
	}
	for index, route := range p.Forwards {
		config.Forwards[index] = tunnel.Forward{
			LocalHost: route.LocalHost, LocalPort: route.LocalPort,
			RemoteHost: route.RemoteBind, RemotePort: route.RemotePort,
		}
	}
	return config
}

func (p profile) validateStorage() error {
	if len(p.Forwards) == 0 || len(p.Forwards) > maxForwards {
		return fmt.Errorf("配置须包含 1–%d 条端口转发。", maxForwards)
	}
	if p.SSHPort < 1 || p.SSHPort > 65535 {
		return errors.New("SSH 端口必须在 1–65535 之间。")
	}
	if p.Auth != tunnel.AuthKey && p.Auth != tunnel.AuthPassword && p.Auth != tunnel.AuthAgent {
		return errors.New("请选择 SSH 认证方式。")
	}
	if p.AutoConnect && p.Auth == tunnel.AuthPassword && (!p.RememberPassword || p.SavedPassword == "") {
		return errors.New("启用登录后自动连接前，请勾选记住 SSH 密码并保存密码。")
	}
	return p.config().ValidateForwards()
}

func saveProfile(path string, p profile) error {
	p.normalizePassword()
	disk := profileFile{Version: profileVersion, profile: p, RememberPassword: p.RememberPassword}
	if p.SavedPassword != "" {
		encryptedPassword, err := encryptProfilePassword(p)
		if err != nil {
			return fmt.Errorf("无法加密保存的 SSH 密码：%w", err)
		}
		disk.EncryptedPassword = encryptedPassword
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxProfileBytes {
		return errors.New("配置文件超过 128 KB。")
	}
	file, err := os.CreateTemp(filepath.Dir(path), "profile-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
