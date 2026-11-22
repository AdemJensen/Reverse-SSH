package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAboutGitHubOpensFixedRepository(t *testing.T) {
	var opened []string
	var browserError error
	c := &Controller{dialogs: Dialogs{OpenURL: func(address string) error {
		opened = append(opened, address)
		return browserError
	}}}
	request := func(method, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, "/api/about/github", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		c.api("api/about/github", result, r)
		if result.Code != want {
			t.Fatalf("GitHub route returned %d, want %d: %s", result.Code, want, result.Body)
		}
		return result.Body.String()
	}

	request(http.MethodGet, "", http.StatusNotFound)
	request(http.MethodPost, `{"url":"https://example.com"}`, http.StatusBadRequest)
	if len(opened) != 0 {
		t.Fatalf("rejected requests opened %v", opened)
	}
	request(http.MethodPost, "{}", http.StatusOK)
	if len(opened) != 1 || opened[0] != "https://github.com/AdemJensen/Reverse-SSH" {
		t.Fatalf("opened wrong repository: %v", opened)
	}

	browserError = errors.New("fixture browser failure")
	if body := request(http.MethodPost, "{}", http.StatusInternalServerError); !strings.Contains(body, browserError.Error()) {
		t.Fatalf("missing browser error: %s", body)
	}
	c.dialogs.OpenURL = nil
	request(http.MethodPost, "{}", http.StatusNotImplemented)
}

func TestHTTPTestServiceAndQuickOpen(t *testing.T) {
	var opened string
	c, err := New(t.TempDir(), false, Dialogs{OpenURL: func(address string) error {
		opened = address
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	post := func(path string, value any, want int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/"+path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		c.api(path, result, r)
		if result.Code != want {
			t.Fatalf("%s returned %d, want %d: %s", path, result.Code, want, result.Body)
		}
		return result
	}

	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { occupied.Close() })
	port := occupied.Addr().(*net.TCPAddr).Port
	endpoint := httpEndpoint{Host: "127.0.0.1", Port: port}
	post("api/test/start", endpoint, http.StatusBadRequest)
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}

	result := post("api/test/start", httpEndpoint{Host: "0.0.0.0", Port: port}, http.StatusOK)
	var state responseState
	if err := json.Unmarshal(result.Body.Bytes(), &state); err != nil || len(state.Tests) != 1 || state.Tests[0] != endpoint {
		t.Fatalf("wrong test server state: %s (%v)", result.Body, err)
	}
	post("api/test/start", httpEndpoint{Host: "localhost", Port: port}, http.StatusOK)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(endpoint.url().String())
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("HTTP 测试成功")) {
		t.Fatalf("unexpected test page: %d %s (%v)", resp.StatusCode, body, err)
	}
	secondPort, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	second := httpEndpoint{Host: "127.0.0.1", Port: secondPort.Addr().(*net.TCPAddr).Port}
	if err := secondPort.Close(); err != nil {
		t.Fatal(err)
	}
	if resp, err := client.Get(second.url().String()); err == nil {
		resp.Body.Close()
		t.Fatal("starting the first test also started the second port")
	}
	result = post("api/test/start", second, http.StatusOK)
	if err := json.Unmarshal(result.Body.Bytes(), &state); err != nil || len(state.Tests) != 2 {
		t.Fatalf("the two test services did not run together: %s (%v)", result.Body, err)
	}

	post("api/open", openRequest{httpEndpoint: endpoint, Side: "local"}, http.StatusOK)
	if opened != endpoint.url().String() {
		t.Fatalf("wrong local URL: %s", opened)
	}
	post("api/open", openRequest{httpEndpoint: httpEndpoint{Host: "::", Port: 8080}, Side: "remote", SSHHost: "[2001:db8::1]"}, http.StatusOK)
	if opened != "http://[2001:db8::1]:8080/" {
		t.Fatalf("wrong remote URL: %s", opened)
	}
	opened = ""
	post("api/open", openRequest{httpEndpoint: endpoint, Side: "remote"}, http.StatusBadRequest)
	if opened != "" {
		t.Fatalf("remote loopback unexpectedly opened local URL %s", opened)
	}
	post("api/open", openRequest{httpEndpoint: httpEndpoint{Host: "example.com/path", Port: 80}, Side: "local"}, http.StatusBadRequest)

	result = post("api/test/stop", httpEndpoint{Host: "localhost", Port: port}, http.StatusOK)
	if err := json.Unmarshal(result.Body.Bytes(), &state); err != nil || len(state.Tests) != 1 || state.Tests[0] != second {
		t.Fatalf("stopping the first test affected the second: %s (%v)", result.Body, err)
	}
	if resp, err := client.Get(endpoint.url().String()); err == nil {
		resp.Body.Close()
		t.Fatal("test listener still accepted HTTP requests after stop")
	}
	resp, err = client.Get(second.url().String())
	if err != nil {
		t.Fatalf("the second test stopped responding: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the second test returned HTTP %d", resp.StatusCode)
	}
}

func TestRemotePreviewUsesBoundDialer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Host+r.URL.RequestURI())
	}))
	t.Cleanup(upstream.Close)
	target := httpEndpoint{Host: "127.0.0.1", Port: 8080}
	preview, err := listenRemotePreview(target, func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(preview.close)
	client := &http.Client{Timeout: 4 * time.Second}
	request := func(host string, want int) string {
		t.Helper()
		r, err := http.NewRequest(http.MethodGet, preview.url+"check?q=ping", nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			r.Host = host
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("unexpected preview response %d %s (%v)", resp.StatusCode, body, err)
		}
		return string(body)
	}
	if body := request("", http.StatusOK); body != target.address()+"/check?q=ping" {
		t.Fatalf("wrong preview upstream request: %s", body)
	}
	if body := request("example.com", http.StatusNotFound); strings.Contains(body, target.address()) {
		t.Fatalf("foreign host was forwarded: %s", body)
	}
}
