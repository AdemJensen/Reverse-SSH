package control

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"
)

const httpCheckTimeout = 5 * time.Second

type checkRequest struct {
	LocalHost  string `json:"localHost"`
	LocalPort  int    `json:"localPort"`
	RemoteBind string `json:"remoteBind"`
	RemotePort int    `json:"remotePort"`
	SSHHost    string `json:"sshHost"`
}

type httpCheckResult struct {
	OK         bool   `json:"ok"`
	Status     int    `json:"status,omitempty"`
	StatusText string `json:"statusText,omitempty"`
	LatencyMS  *int64 `json:"latencyMs,omitempty"`
	Error      string `json:"error,omitempty"`
	ViaSSH     bool   `json:"viaSSH,omitempty"`
}

type checkResult struct {
	Local  httpCheckResult `json:"local"`
	Remote httpCheckResult `json:"remote"`
}

func (c *Controller) checkEndpoints(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	local := openRequest{Side: "local", httpEndpoint: httpEndpoint{Host: req.LocalHost, Port: req.LocalPort}}
	remote := openRequest{
		Side: "remote", SSHHost: req.SSHHost,
		httpEndpoint: httpEndpoint{Host: req.RemoteBind, Port: req.RemotePort},
	}
	localDone := make(chan httpCheckResult, 1)
	go func() { localDone <- c.checkHTTP(r.Context(), local) }()
	remoteResult := c.checkHTTP(r.Context(), remote)
	writeJSON(w, checkResult{Local: <-localDone, Remote: remoteResult})
}

func (c *Controller) checkHTTP(ctx context.Context, req openRequest) httpCheckResult {
	endpoint, viaSSH, err := req.resolve()
	if err != nil {
		return httpCheckResult{Error: err.Error()}
	}
	result := httpCheckResult{ViaSSH: viaSSH}
	ctx, cancel := context.WithTimeout(ctx, httpCheckTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.url().String(), nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	transport := &http.Transport{DisableKeepAlives: true}
	if viaSSH {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return c.manager.DialRemote(ctx, endpoint.Host, endpoint.Port)
		}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	started := time.Now()
	response, err := client.Do(request)
	latency := time.Since(started).Milliseconds()
	result.LatencyMS = &latency
	if err != nil {
		result.Error = describeHTTPCheckError(err)
		return result
	}
	response.Body.Close()
	result.Status = response.StatusCode
	result.StatusText = http.StatusText(response.StatusCode)
	result.OK = response.StatusCode >= 200 && response.StatusCode < 300
	return result
}

func describeHTTPCheckError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "等待 HTTP 响应超时。"
	case errors.Is(err, context.Canceled):
		return "检测已取消。"
	}
	var requestError *url.Error
	if errors.As(err, &requestError) {
		return requestError.Err.Error()
	}
	return err.Error()
}
