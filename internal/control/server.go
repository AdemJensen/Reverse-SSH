package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"reversessh/internal/startup"
	"reversessh/internal/tunnel"
)

type Controller struct {
	mu          sync.Mutex
	manager     *tunnel.Manager
	profile     profile
	profilePath string
	pending     *connectRequest
	appMessage  string
	lastLog     logEntry
	logs        []logEntry
	dialogs     Dialogs
	tests       map[httpEndpoint]*listeningHTTPServer
	previews    map[httpEndpoint]*remotePreview
}

type Dialogs struct {
	PickKey func(string) (string, error)
	Import  func(string) (string, error)
	Export  func(string, string) (string, error)
	OpenURL func(string) error
}

type connectRequest struct {
	profile
	Password   string `json:"password"`
	Passphrase string `json:"passphrase"`
}

func (r connectRequest) secrets() tunnel.Secrets {
	return tunnel.Secrets{Password: r.Password, KeyPassphrase: r.Passphrase}
}

type logEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type hostKey struct {
	Host        string `json:"host"`
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
}

type connectionStatus struct {
	State           string   `json:"state"`
	Message         string   `json:"message"`
	RemoteAddresses []string `json:"remoteAddresses,omitempty"`
	HostKey         *hostKey `json:"hostKey,omitempty"`
}

type responseState struct {
	Profile                  profile          `json:"profile"`
	Status                   connectionStatus `json:"status"`
	AutoStart                bool             `json:"autoStart"`
	AutoStartError           string           `json:"autoStartError,omitempty"`
	HasSavedPassword         bool             `json:"hasSavedPassword"`
	SavedPasswordUnavailable bool             `json:"savedPasswordUnavailable,omitempty"`
	Logs                     []logEntry       `json:"logs,omitempty"`
	Tests                    []httpEndpoint   `json:"tests"`
}

func New(dataDir string, autoConnect bool, dialogs Dialogs) (*Controller, error) {
	path := filepath.Join(dataDir, "profile.json")
	p, err := loadProfile(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取连接配置 %s：%w", path, err)
	}
	c := &Controller{
		manager: tunnel.New(filepath.Join(dataDir, "known_hosts")),
		profile: p, profilePath: path, dialogs: dialogs,
		tests: make(map[httpEndpoint]*listeningHTTPServer), previews: make(map[httpEndpoint]*remotePreview),
	}
	initial := c.manager.Snapshot()
	c.lastLog = logEntry{Level: initial.State, Message: initial.Message}
	if !autoConnect || !p.AutoConnect {
		return c, nil
	}
	if p.Auth == tunnel.AuthPassword && !p.hasSavedPassword() {
		c.appMessage = "此连接需要 SSH 密码，请输入密码后连接。"
		return c, nil
	}
	if err := c.connect(connectRequest{profile: p}, false); err != nil {
		c.appMessage = "自动连接未启动：" + err.Error()
	}
	return c, nil
}

func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = nil
	c.closePreviews()
	for endpoint, server := range c.tests {
		server.close()
		delete(c.tests, endpoint)
	}
	c.manager.Stop()
}

func (c *Controller) Listen(assets fs.FS) (*http.Server, string, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	var entropy [24]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		listener.Close()
		return nil, "", err
	}
	prefix := "/" + hex.EncodeToString(entropy[:]) + "/"
	host := listener.Addr().String()
	origin := "http://" + host
	static := http.FileServer(http.FS(assets))
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       35 * time.Second,
		MaxHeaderBytes:    16 << 10,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != host || !strings.HasPrefix(r.URL.Path, prefix) || r.URL.RawPath != "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
			path := strings.TrimPrefix(r.URL.Path, prefix)
			if strings.HasPrefix(path, "api/") {
				if r.Method != http.MethodGet && !allowedMutation(r, origin) {
					writeError(w, http.StatusForbidden, errors.New("当前请求未通过本机界面校验。请退出应用后重新打开。"))
					return
				}
				c.api(path, w, r)
				return
			}
			if path == "" || path == "app.css" || path == "app.js" {
				r.URL.Path = "/" + path
				static.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
		}),
	}
	go server.Serve(listener)
	return server, origin + prefix, nil
}

func allowedMutation(r *http.Request, origin string) bool {
	if r.Header.Get("X-Reverse-SSH") != "desktop" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	// WebKit may send a null Origin with Referrer-Policy: no-referrer.
	// A foreign page cannot send the required custom header without a CORS preflight.
	from := r.Header.Get("Origin")
	return from == "" || from == "null" || from == origin
}

func (c *Controller) api(path string, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		if r.Header.Get("Content-Type") != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, errors.New("请求需要 JSON 内容。"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxProfileBytes)
	}
	// Dialogs, browser launches and HTTP checks can wait without locking app state.
	switch r.Method + " " + path {
	case "POST api/pick-key":
		c.chooseKey(w)
		return
	case "POST api/import":
		c.importConfiguration(w, r)
		return
	case "POST api/import-command":
		importSSHCommand(w, r)
		return
	case "POST api/export":
		c.exportConfiguration(w, r)
		return
	case "POST api/open":
		c.openEndpoint(w, r)
		return
	case "POST api/about/github":
		c.openProjectLink(w, r, projectRepositoryURL)
		return
	case "POST api/about/website":
		c.openProjectLink(w, r, projectWebsiteURL)
		return
	case "POST api/check":
		c.checkEndpoints(w, r)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	switch r.Method + " " + path {
	case "GET api/state":
		writeJSON(w, c.state())
		return
	case "PUT api/profile":
		err = c.updateProfile(r)
	case "POST api/connect":
		var req connectRequest
		if err = decode(r, &req); err == nil {
			err = c.connect(req, true)
		}
	case "POST api/disconnect":
		c.pending = nil
		c.appMessage = ""
		c.closePreviews()
		c.manager.Stop()
	case "POST api/test/start", "POST api/test/stop":
		err = c.setHTTPTest(r, path == "api/test/start")
	case "POST api/trust":
		var req struct {
			Fingerprint string `json:"fingerprint"`
		}
		if err = decode(r, &req); err == nil {
			err = c.trust(req.Fingerprint)
		}
	case "POST api/autostart":
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err = decode(r, &req); err == nil {
			if req.Enabled == nil {
				err = errors.New("请选择是否启用登录时启动。")
			} else if e := startup.SetEnabled(*req.Enabled); e != nil {
				err = fmt.Errorf("无法更新登录启动：%w", e)
			}
		}
	default:
		writeError(w, http.StatusNotFound, errors.New("接口不存在。"))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, c.state())
}

func (c *Controller) chooseKey(w http.ResponseWriter) {
	if c.dialogs.PickKey == nil {
		writeError(w, http.StatusNotImplemented, errors.New("当前系统无法打开文件选择器。"))
		return
	}
	c.mu.Lock()
	path := c.profile.KeyPath
	c.mu.Unlock()
	dir := ""
	if path != "" && filepath.IsAbs(path) {
		dir = filepath.Dir(path)
	}
	selected, err := c.dialogs.PickKey(dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"path": selected})
}

func (c *Controller) connectionInProgress() bool {
	state := c.manager.Snapshot().State
	return state == tunnel.StateConnecting || state == tunnel.StateConnected || state == tunnel.StateReconnecting
}

func (c *Controller) connect(req connectRequest, persist bool) error {
	req.normalize()
	if len(req.Forwards) == 0 || len(req.Forwards) > maxForwards {
		return fmt.Errorf("配置须包含 1–%d 条端口转发。", maxForwards)
	}
	config := req.config()
	if err := config.Validate(); err != nil {
		return err
	}
	if c.connectionInProgress() {
		return errors.New("请先断开当前连接。")
	}
	if err := c.applyPassword(&req.profile, req.Password); err != nil {
		return err
	}
	if req.Auth == tunnel.AuthPassword && req.Password == "" {
		req.Password = req.SavedPassword
	}
	if req.Auth == tunnel.AuthPassword && req.Password == "" {
		return errors.New("请输入 SSH 密码。")
	}
	if persist {
		if err := c.save(req.profile); err != nil {
			return err
		}
	}
	if err := c.manager.Start(config, req.secrets()); err != nil {
		return err
	}
	c.closePreviews()
	c.appMessage = ""
	c.pending = &req
	return nil
}

func (c *Controller) save(p profile) error {
	if err := saveProfile(c.profilePath, p); err != nil {
		return fmt.Errorf("无法保存连接配置：%w", err)
	}
	c.profile = p
	return nil
}

func (c *Controller) trust(fingerprint string) error {
	if c.pending == nil {
		return errors.New("没有待确认的服务器指纹。")
	}
	if err := c.manager.TrustHost(fingerprint); err != nil {
		return err
	}
	return c.manager.Start(c.pending.config(), c.pending.secrets())
}

func (c *Controller) state() responseState {
	s := c.manager.Snapshot()
	status := connectionStatus{State: s.State, Message: s.Message, RemoteAddresses: s.RemoteAddresses}
	if s.HostKey != nil {
		status.HostKey = &hostKey{Host: s.HostKey.Host, Algorithm: s.HostKey.Type, Fingerprint: s.HostKey.Fingerprint}
	}
	if status.State == tunnel.StateStopped && c.appMessage != "" {
		status.Message = c.appMessage
	}
	if status.State == tunnel.StateError || status.State == tunnel.StateStopped ||
		(status.State == tunnel.StateConnected && c.pending != nil && !c.pending.Reconnect) {
		c.pending = nil
	}
	if status.Message != "" && (c.lastLog.Level != status.State || c.lastLog.Message != status.Message) {
		c.lastLog = logEntry{Time: time.Now(), Level: status.State, Message: status.Message}
		c.logs = append(c.logs, c.lastLog)
		if len(c.logs) > 32 {
			c.logs = slices.Delete(c.logs, 0, len(c.logs)-32)
		}
	}
	resp := responseState{
		Profile: c.profile, Status: status, Logs: c.logs, Tests: c.testEndpoints(),
		HasSavedPassword:         c.profile.hasSavedPassword(),
		SavedPasswordUnavailable: c.profile.SavedPasswordUnavailable,
	}
	var err error
	resp.AutoStart, err = startup.Enabled()
	if err != nil {
		resp.AutoStartError = err.Error()
	}
	return resp
}

func decode(r *http.Request, destination any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("请求格式无效。")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("请求包含多余内容。")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, value any) {
	json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	writeJSON(w, map[string]string{"error": err.Error()})
}
