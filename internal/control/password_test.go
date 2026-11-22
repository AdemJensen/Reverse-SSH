package control

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRememberedPasswordIsReusedAndCanBeExported(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	dir := t.TempDir()
	exported := filepath.Join(t.TempDir(), "export.json")
	c, err := New(dir, false, Dialogs{Export: func(string, string) (string, error) { return exported, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	p := defaultProfile()
	p.Host, p.User, p.Auth = "127.0.0.1", "fixture", "password"
	p.SSHPort = listener.Addr().(*net.TCPAddr).Port
	p.RememberPassword, p.AutoConnect = true, true
	const secret = " fixture password with spaces "
	request := struct {
		profile
		Password string `json:"password"`
	}{profile: p, Password: secret}

	res := controllerJSON(t, c, http.MethodPut, "api/profile", request, http.StatusOK)
	var state responseState
	if err := json.Unmarshal(res, &state); err != nil || !state.HasSavedPassword || !state.Profile.RememberPassword || bytes.Contains(res, []byte(secret)) {
		t.Fatal("profile response should report the stored password without disclosing it")
	}
	res = controllerJSON(t, c, http.MethodGet, "api/state", nil, http.StatusOK)
	if bytes.Contains(res, []byte(secret)) {
		t.Fatal("state disclosed the saved password")
	}
	request.Password = ""
	controllerJSON(t, c, http.MethodPut, "api/profile", request, http.StatusOK)
	local, err := loadProfile(filepath.Join(dir, "profile.json"))
	if err != nil || local.SavedPassword != secret || !local.AutoConnect {
		t.Fatal("same-identity save did not retain the exact password")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil || bytes.Contains(raw, []byte(secret)) {
		t.Fatal("the saved profile did not protect its password")
	}
	var disk map[string]json.RawMessage
	if err := json.Unmarshal(raw, &disk); err != nil || len(disk["encryptedPassword"]) == 0 || disk["password"] != nil {
		t.Fatal("the saved profile did not contain only an encrypted credential")
	}
	controllerJSON(t, c, http.MethodPost, "api/export", p, http.StatusOK)
	portable, err := readProfile(exported, true)
	if err != nil || portable.SavedPassword != secret || !portable.RememberPassword || !portable.AutoConnect {
		t.Fatal("portable export did not retain the saved password and its connection settings")
	}
	raw, err = os.ReadFile(exported)
	disk = nil
	if err != nil || bytes.Contains(raw, []byte(secret)) || json.Unmarshal(raw, &disk) != nil ||
		len(disk["encryptedPassword"]) == 0 || disk["password"] != nil {
		t.Fatal("portable export did not contain only an encrypted credential")
	}
	other, err := New(t.TempDir(), false, Dialogs{Import: func(string) (string, error) { return exported, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	res = controllerJSON(t, other, http.MethodPost, "api/import", struct{}{}, http.StatusOK)
	var imported importResult
	if err := json.Unmarshal(res, &imported); err != nil || !imported.Imported || !imported.State.HasSavedPassword || bytes.Contains(res, []byte(secret)) {
		t.Fatal("ordinary import did not restore the credential without exposing it")
	}
	controllerJSON(t, other, http.MethodPost, "api/connect", connectRequest{profile: p}, http.StatusOK)
	if other.pending == nil || other.pending.Password != secret {
		t.Fatal("imported configuration could not connect with an empty password field")
	}
	controllerJSON(t, other, http.MethodPost, "api/disconnect", struct{}{}, http.StatusOK)

	wrongIdentity := p
	wrongIdentity.User = "different-user"
	unremembered := p
	unremembered.RememberPassword = false
	for _, draft := range []profile{wrongIdentity, unremembered} {
		controllerJSON(t, c, http.MethodPost, "api/export", draft, http.StatusOK)
		portable, err = readProfile(exported, true)
		if err != nil || portable.SavedPassword != "" || portable.RememberPassword || portable.AutoConnect {
			t.Fatal("a new identity or a draft without password storage exported a saved credential")
		}
	}
	controllerJSON(t, c, http.MethodPost, "api/connect", connectRequest{profile: wrongIdentity}, http.StatusBadRequest)
	res = controllerJSON(t, c, http.MethodPost, "api/connect", connectRequest{profile: p}, http.StatusOK)
	if bytes.Contains(res, []byte(secret)) || c.pending == nil || c.pending.Password != secret {
		t.Fatal("manual connection failed to reuse the password safely")
	}
	controllerJSON(t, c, http.MethodPost, "api/disconnect", struct{}{}, http.StatusOK)
	c.Close()
	background, err := New(dir, true, Dialogs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(background.Close)
	if background.pending == nil || background.pending.Password != secret {
		t.Fatal("login connection did not reuse the saved password")
	}
	controllerJSON(t, background, http.MethodPost, "api/disconnect", struct{}{}, http.StatusOK)
	p.RememberPassword = false
	controllerJSON(t, background, http.MethodPut, "api/profile", p, http.StatusOK)
	local, err = loadProfile(filepath.Join(dir, "profile.json"))
	if err != nil || local.SavedPassword != "" || local.RememberPassword || local.AutoConnect {
		t.Fatal("turning off password storage did not remove the secret and password auto-connect")
	}
}

func TestSSHCommandImportReturnsDraftWithExactBind(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, false, Dialogs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	command := `ssh -NT \
  -p 7022 \
  -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 \
  -o ServerAliveCountMax=3 \
  -R 0.0.0.0:10001:127.0.0.1:8080 \
  tunnel@example.test`
	res := controllerJSON(t, c, http.MethodPost, "api/import-command", map[string]string{"command": command}, http.StatusOK)
	var imported commandImport
	if err := json.Unmarshal(res, &imported); err != nil {
		t.Fatal(err)
	}
	want := forward{RemoteBind: "0.0.0.0", RemotePort: 10001, LocalHost: "127.0.0.1", LocalPort: 8080}
	p := imported.Settings
	if p.Host != "example.test" || p.SSHPort != 7022 || p.User != "tunnel" || len(p.Forwards) != 1 || p.Forwards[0] != want {
		t.Fatalf("unexpected draft: %+v", p)
	}
	if c.profile.Host != "" {
		t.Fatal("command import changed the saved profile")
	}
	if _, err := os.Stat(filepath.Join(dir, "profile.json")); !os.IsNotExist(err) {
		t.Fatalf("command import unexpectedly wrote a profile: %v", err)
	}
}

func controllerJSON(t *testing.T, c *Controller, method, path string, payload any, status int) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "/"+path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	c.api(path, response, r)
	if response.Code != status {
		t.Fatalf("%s %s got HTTP %d, want %d", method, path, response.Code, status)
	}
	return response.Body.Bytes()
}
