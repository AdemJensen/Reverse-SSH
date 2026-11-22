package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"reversessh/internal/tunnel"
)

type remotePreview struct {
	url       string
	server    *listeningHTTPServer
	transport *http.Transport
}

func (c *Controller) previewRemote(ctx context.Context, endpoint httpEndpoint) (string, error) {
	dial := func(ctx context.Context) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		return c.manager.DialRemote(ctx, endpoint.Host, endpoint.Port)
	}
	// Check SSH forwarding permission before opening a browser tab.
	probe, err := dial(ctx)
	if err != nil {
		return "", err
	}
	probe.Close()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.manager.Snapshot().State != tunnel.StateConnected {
		return "", errors.New("SSH 隧道已断开，请连接后再打开远端地址。")
	}
	if existing := c.previews[endpoint]; existing != nil {
		return existing.url, nil
	}
	preview, err := listenRemotePreview(endpoint, dial)
	if err != nil {
		return "", fmt.Errorf("无法创建远端 HTTP 预览：%w", err)
	}
	c.previews[endpoint] = preview
	return preview.url, nil
}

func listenRemotePreview(endpoint httpEndpoint, dial func(context.Context) (net.Conn, error)) (*remotePreview, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	target := endpoint.url()
	transport := &http.Transport{
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "无法访问远端 HTTP 服务。请确认 SSH 隧道已连接，并检查本地服务是否正在运行。", http.StatusBadGateway)
		},
	}
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       35 * time.Second,
		MaxHeaderBytes:    32 << 10,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != address {
				http.NotFound(w, r)
				return
			}
			if r.Method == http.MethodConnect || r.Header.Get("Upgrade") != "" {
				http.Error(w, "远端预览仅支持普通 HTTP 请求。", http.StatusBadRequest)
				return
			}
			proxy.ServeHTTP(w, r)
		}),
	}
	go server.Serve(listener)
	return &remotePreview{
		url: "http://" + address + "/", server: &listeningHTTPServer{server: server, listener: listener}, transport: transport,
	}, nil
}

func (p *remotePreview) close() {
	p.server.close()
	p.transport.CloseIdleConnections()
}

func (c *Controller) closePreviews() {
	for endpoint, preview := range c.previews {
		preview.close()
		delete(c.previews, endpoint)
	}
}
