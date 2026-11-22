package control

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"reversessh/internal/tunnel"
)

type httpEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (e httpEndpoint) address() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

func (e httpEndpoint) url() *url.URL {
	return &url.URL{Scheme: "http", Host: e.address(), Path: "/"}
}

func (e *httpEndpoint) normalize() error {
	var err error
	e.Host, err = tunnel.NormalizeForwardEndpoint(e.Host, e.Port)
	return err
}

func (e *httpEndpoint) replaceWildcard() {
	switch e.Host {
	case "0.0.0.0":
		e.Host = "127.0.0.1"
	case "::":
		e.Host = "::1"
	}
}

func (e httpEndpoint) loopback() bool {
	if strings.TrimSuffix(e.Host, ".") == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(e.Host)
	return err == nil && ip.IsLoopback()
}

type openRequest struct {
	httpEndpoint
	Side    string `json:"side"`
	SSHHost string `json:"sshHost"`
}

type openResult struct {
	URL    string `json:"url"`
	ViaSSH bool   `json:"viaSSH,omitempty"`
}

const projectRepositoryURL = "https://github.com/AdemJensen/Reverse-SSH"

func (c *Controller) openProjectRepository(w http.ResponseWriter, r *http.Request) {
	if err := decode(r, &struct{}{}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if c.dialogs.OpenURL == nil {
		writeError(w, http.StatusNotImplemented, errors.New("当前系统无法打开默认浏览器。"))
		return
	}
	if err := c.dialogs.OpenURL(projectRepositoryURL); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, struct{}{})
}

func (c *Controller) openEndpoint(w http.ResponseWriter, r *http.Request) {
	var req openRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if c.dialogs.OpenURL == nil {
		writeError(w, http.StatusNotImplemented, errors.New("当前系统无法打开默认浏览器。"))
		return
	}
	result, err := c.browserAddress(r.Context(), req)
	if err == nil {
		err = c.dialogs.OpenURL(result.URL)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, result)
}

func (c *Controller) browserAddress(ctx context.Context, req openRequest) (openResult, error) {
	endpoint, viaSSH, err := req.resolve()
	if err != nil {
		return openResult{}, err
	}
	if viaSSH {
		address, err := c.previewRemote(ctx, endpoint)
		return openResult{URL: address, ViaSSH: true}, err
	}
	return openResult{URL: endpoint.url().String()}, nil
}

func (req openRequest) resolve() (httpEndpoint, bool, error) {
	if err := req.normalize(); err != nil {
		return httpEndpoint{}, false, err
	}
	viaSSH := false
	switch req.Side {
	case "local":
		req.replaceWildcard()
	case "remote":
		viaSSH = req.loopback()
		if req.Host == "0.0.0.0" || req.Host == "::" {
			if strings.TrimSpace(req.SSHHost) == "" {
				return httpEndpoint{}, false, errors.New("请填写 SSH 服务器地址后再访问远端端口。")
			}
			req.Host = req.SSHHost
			if err := req.normalize(); err != nil {
				return httpEndpoint{}, false, err
			}
		}
	default:
		return httpEndpoint{}, false, errors.New("请选择远端或本地地址。")
	}
	return req.httpEndpoint, viaSSH, nil
}

func (c *Controller) setHTTPTest(r *http.Request, enabled bool) error {
	var endpoint httpEndpoint
	if err := decode(r, &endpoint); err != nil {
		return err
	}
	if err := endpoint.normalize(); err != nil {
		return err
	}
	endpoint.replaceWildcard()
	if strings.TrimSuffix(endpoint.Host, ".") == "localhost" {
		endpoint.Host = "127.0.0.1"
	}
	if current := c.tests[endpoint]; current != nil {
		if !enabled {
			delete(c.tests, endpoint)
			return current.close()
		}
		return nil
	}
	if !enabled {
		return nil
	}
	if len(c.tests) >= maxForwards {
		return fmt.Errorf("最多可同时运行 %d 个测试服务。", maxForwards)
	}
	server, err := listenHTTPTest(r.Context(), endpoint)
	if err != nil {
		return err
	}
	c.tests[endpoint] = server
	return nil
}

func (c *Controller) testEndpoints() []httpEndpoint {
	endpoints := make([]httpEndpoint, 0, len(c.tests))
	for endpoint := range c.tests {
		endpoints = append(endpoints, endpoint)
	}
	sort.Slice(endpoints, func(i, j int) bool {
		if endpoints[i].Port != endpoints[j].Port {
			return endpoints[i].Port < endpoints[j].Port
		}
		return endpoints[i].Host < endpoints[j].Host
	})
	return endpoints
}

type listeningHTTPServer struct {
	server   *http.Server
	listener net.Listener
}

func (s *listeningHTTPServer) close() error {
	err := s.server.Close()
	// Close also handles the interval before Serve records the listener.
	s.listener.Close()
	return err
}

func listenHTTPTest(ctx context.Context, endpoint httpEndpoint) (*listeningHTTPServer, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var config net.ListenConfig
	address := endpoint.address()
	listener, err := config.Listen(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("无法在 %s 启动测试服务：%w", address, err)
	}
	if listener.Addr().(*net.TCPAddr).IP.IsUnspecified() {
		listener.Close()
		return nil, errors.New("测试服务需要明确的本机监听地址。")
	}
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    16 << 10,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
			testPage.Execute(w, address)
		}),
	}
	go server.Serve(listener)
	return &listeningHTTPServer{server: server, listener: listener}, nil
}

var testPage = template.Must(template.New("http-test").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Reverse SSH · HTTP 测试</title><style>
body { background: #f3f8f6; color: #172532; font: 16px/1.6 system-ui, sans-serif; margin: 0; padding: 2rem; }
main { max-width: 30rem; margin: 10vh auto; padding: 2rem; border: 1px solid #d5e5df; border-radius: 1rem; background: white; }
h1 { color: #176957; font-size: 1.6rem; margin: 0 0 .6rem; }
p { margin: 0; }
</style></head><body><main><h1>HTTP 测试成功</h1><p>本地测试服务 <code>{{.}}</code></p></main></body></html>`))
