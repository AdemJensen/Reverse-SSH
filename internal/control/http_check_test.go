package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPCheckReportsBothSidesWithoutFollowingRedirects(t *testing.T) {
	var localRequests atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localRequests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/" {
			http.Error(w, "unexpected HTTP request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(local.Close)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, local.URL+"/should-not-be-visited", http.StatusFound)
	}))
	t.Cleanup(remote.Close)
	c, err := New(t.TempDir(), false, Dialogs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)

	check := func(req checkRequest) checkResult {
		t.Helper()
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/check", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		c.api("api/check", response, r)
		var result checkResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
			t.Fatalf("unexpected check response: HTTP %d %s (%v)", response.Code, response.Body, err)
		}
		return result
	}

	req := checkRequest{
		LocalHost: "0.0.0.0", LocalPort: local.Listener.Addr().(*net.TCPAddr).Port,
		RemoteBind: "0.0.0.0", RemotePort: remote.Listener.Addr().(*net.TCPAddr).Port,
		SSHHost: "127.0.0.1",
	}
	result := check(req)
	if !result.Local.OK || result.Local.Status != http.StatusCreated || result.Local.LatencyMS == nil || result.Local.Error != "" {
		t.Fatalf("unexpected local result: %+v", result.Local)
	}
	if result.Remote.OK || result.Remote.Status != http.StatusFound || result.Remote.ViaSSH || result.Remote.LatencyMS == nil || result.Remote.Error != "" {
		t.Fatalf("unexpected direct remote result: %+v", result.Remote)
	}
	if count := localRequests.Load(); count != 1 {
		t.Fatalf("local handler received %d requests; redirect may have been followed", count)
	}

	req.RemoteBind = "127.0.0.1"
	result = check(req)
	if !result.Local.OK || result.Local.Status != http.StatusCreated {
		t.Fatalf("missing SSH affected the local check: %+v", result.Local)
	}
	if result.Remote.OK || result.Remote.Status != 0 || !result.Remote.ViaSSH || !strings.Contains(result.Remote.Error, "SSH 隧道尚未连接") {
		t.Fatalf("unexpected disconnected remote result: %+v", result.Remote)
	}
	if len(c.testEndpoints()) != 0 {
		t.Fatal("connection checking started a test listener")
	}
}

func TestHTTPCheckReportsTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)
	c, err := New(t.TempDir(), false, Dialogs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result := c.checkHTTP(ctx, openRequest{
		Side: "local", httpEndpoint: httpEndpoint{Host: "127.0.0.1", Port: upstream.Listener.Addr().(*net.TCPAddr).Port},
	})
	if result.OK || result.Status != 0 || result.LatencyMS == nil || result.Error != "等待 HTTP 响应超时。" {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
}
