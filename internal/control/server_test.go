package control

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reversessh/internal/ui"
)

func TestLocalInterfaceAndLoginSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	exportPath := filepath.Join(t.TempDir(), "export.json")
	var importPath atomic.Value
	importPath.Store("")
	c, err := New(dir, false, Dialogs{
		PickKey: func(string) (string, error) { return "fixture-key", nil },
		Export:  func(string, string) (string, error) { return exportPath, nil },
		Import:  func(string) (string, error) { return importPath.Load().(string), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	server, address, err := c.Listen(ui.Assets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	entry, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	origin := entry.Scheme + "://" + entry.Host
	client := &http.Client{Timeout: 4 * time.Second}
	request := func(method, path, from, data string, want int, adjust ...func(*http.Request)) []byte {
		t.Helper()
		r, err := http.NewRequest(method, address+path, strings.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if method != http.MethodGet {
			r.Header.Set("Origin", from)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Reverse-SSH", "desktop")
		}
		for _, change := range adjust {
			change(r)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: HTTP %d, %s, %v", method, path, resp.StatusCode, body, err)
		}
		return body
	}
	if page := request("GET", "", "", "", 200); !bytes.Contains(page, []byte("用 SSH 把本机服务发布到公网")) {
		t.Fatal("bundled UI did not render")
	}
	request("GET", "app.css", "", "", 200)
	request("GET", "app.js", "", "", 200)
	if selected := request("POST", "api/pick-key", origin, "{}", 200); !bytes.Contains(selected, []byte("fixture-key")) {
		t.Fatalf("key selection did not return its path: %s", selected)
	}
	request("POST", "api/autostart", "https://example.com", `{"enabled":true}`, 403)
	request("POST", "api/autostart", "null", `{"enabled":true}`, 403, func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	request("POST", "api/autostart", origin, `{"enabled":true}`, 403, func(r *http.Request) {
		r.Header.Del("X-Reverse-SSH")
	})
	if runtime.GOOS == "darwin" {
		var state responseState
		body := request("POST", "api/autostart", "null", `{"enabled":true}`, 200)
		if err := json.Unmarshal(body, &state); err != nil || !state.AutoStart {
			t.Fatalf("login startup registration missing: %s, %v", body, err)
		}
		body = request("POST", "api/autostart", "", `{"enabled":false}`, 200)
		if err := json.Unmarshal(body, &state); err != nil || state.AutoStart {
			t.Fatalf("login startup registration remained: %s, %v", body, err)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	p := profile{
		Host: "127.0.0.1", SSHPort: listener.Addr().(*net.TCPAddr).Port, User: "fixture",
		Auth: "password", Forwards: []forward{
			{RemoteBind: "127.0.0.1", RemotePort: 8080, LocalHost: "127.0.0.1", LocalPort: 3000},
			{RemoteBind: "127.0.0.1", RemotePort: 8081, LocalHost: "127.0.0.1", LocalPort: 3001},
		},
	}
	data, err := json.Marshal(connectRequest{profile: p, Password: "fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	request("POST", "api/connect", origin, string(data), 200)
	request("POST", "api/import", origin, "{}", 409)
	request("POST", "api/disconnect", origin, "{}", 200)
	saved, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk profile
	if err := json.Unmarshal(saved, &disk); err != nil || !reflect.DeepEqual(disk, p) || bytes.Contains(saved, []byte("fixture-password")) {
		t.Fatalf("invalid profile persistence: %s, %v", saved, err)
	}
	// Exports include the draft and all forwards, while preserving the saved profile.
	draft := p
	draft.Host = "draft.example.com"
	draftJSON, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	var exported exportResult
	body := request("POST", "api/export", origin, string(draftJSON), 200)
	if err := json.Unmarshal(body, &exported); err != nil || !exported.Saved {
		t.Fatalf("export did not save: %s, %v", body, err)
	}
	existing, err := loadProfile(filepath.Join(dir, "profile.json"))
	if err != nil || !reflect.DeepEqual(existing, p) {
		t.Fatalf("export changed the saved profile: %+v, %v", existing, err)
	}
	external, err := readProfile(exportPath, true)
	if err != nil || !reflect.DeepEqual(external, draft) {
		t.Fatalf("export did not round trip: %+v, %v", external, err)
	}
	var imported importResult
	body = request("POST", "api/import", origin, "{}", 200)
	if err := json.Unmarshal(body, &imported); err != nil || imported.Imported || !reflect.DeepEqual(imported.State.Profile, p) {
		t.Fatalf("cancelled import changed the saved profile: %s, %v", body, err)
	}
	importPath.Store(exportPath)
	body = request("POST", "api/import", origin, "{}", 200)
	if err := json.Unmarshal(body, &imported); err != nil || !imported.Imported || !reflect.DeepEqual(imported.State.Profile, draft) {
		t.Fatalf("import did not apply the exported profile: %s, %v", body, err)
	}
	if err := os.WriteFile(exportPath, []byte(`{"version":2,"forwards":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	request("POST", "api/import", origin, "{}", 400)
	existing, err = loadProfile(filepath.Join(dir, "profile.json"))
	if err != nil || !reflect.DeepEqual(existing, draft) {
		t.Fatalf("invalid import changed the saved profile: %+v, %v", existing, err)
	}
}
