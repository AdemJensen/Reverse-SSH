package control

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"reversessh/internal/tunnel"
)

func TestReadLegacySingleForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-profile.json")
	const old = `{
	  "host":"server.example.com", "sshPort":2222, "user":"operator",
	  "remoteBind":"::1", "remotePort":9000, "localHost":"localhost", "localPort":4000,
	  "auth":"key", "keyPath":"~/.ssh/id_ed25519", "reconnect":false, "autoConnect":true
	}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	for _, importing := range []bool{false, true} {
		p, err := readProfile(path, importing)
		if err != nil {
			t.Fatal(err)
		}
		expected := forward{RemoteBind: "::1", RemotePort: 9000, LocalHost: "localhost", LocalPort: 4000}
		if len(p.Forwards) != 1 || p.Forwards[0] != expected {
			t.Fatalf("legacy route did not migrate: %+v", p.Forwards)
		}
		if p.Host != "server.example.com" || p.SSHPort != 2222 || p.User != "operator" ||
			p.KeyPath != "~/.ssh/id_ed25519" || p.Reconnect || !p.AutoConnect {
			t.Fatalf("legacy connection values did not carry forward: %+v", p)
		}
	}
}

func TestReadPasswordProfileBeforeRememberPasswordWasAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	const old = `{
	  "version":2, "host":"server.example.com", "user":"operator", "auth":"password",
	  "forwards":[{"remotePort":9000,"localPort":4000}]
	}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := loadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Auth != tunnel.AuthPassword || p.RememberPassword || p.SavedPassword != "" {
		t.Fatal("legacy password profile did not retain manual password authentication")
	}
}

func TestRememberedPasswordProfileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	p := defaultProfile()
	p.Host = "[2001:0DB8:0:0::1]"
	p.User = "operator"
	p.Auth = tunnel.AuthPassword
	p.AutoConnect = true
	p.RememberPassword = true
	const secret = " fixture-password "
	p.SavedPassword = secret
	if err := p.validateStorage(); err != nil {
		t.Fatal(err)
	}
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	data, encrypted := readEncryptedProfile(t, path)
	if len(encrypted) != 12+len(secret)+16 || bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(`"password":`)) {
		t.Fatal("local storage did not contain only the AES-GCM ciphertext")
	}
	// An equivalent canonical IPv6 spelling authenticates the same SSH identity.
	copyPath := filepath.Join(t.TempDir(), "profile-copy.json")
	canonicalHost := bytes.Replace(data, []byte(`"[2001:0DB8:0:0::1]"`), []byte(`"2001:db8::1"`), 1)
	if err := os.WriteFile(copyPath, canonicalHost, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	_, encryptedAgain := readEncryptedProfile(t, path)
	if bytes.Equal(encrypted[:12], encryptedAgain[:12]) {
		t.Fatal("successive saves used the same nonce")
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 1 {
		t.Fatal("saving generated an unexpected file alongside the profile")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("profile mode = %04o, want 0600", info.Mode().Perm())
	}
	stored, err := loadProfile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Host != "2001:db8::1" || !stored.AutoConnect || !stored.RememberPassword || stored.SavedPassword != secret {
		t.Fatal("remembered password did not round trip through local storage")
	}
	apiProfile, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(apiProfile, []byte(secret)) || bytes.Contains(apiProfile, []byte(`"password":`)) ||
		bytes.Contains(apiProfile, []byte(`"encryptedPassword":`)) {
		t.Fatal("profile JSON exposed the stored password")
	}
	if !bytes.Contains(apiProfile, []byte(`"rememberPassword":true`)) {
		t.Fatal("profile JSON did not include the opt-in state")
	}
	imported, err := readProfile(copyPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Host != "2001:db8::1" || imported.SavedPassword != secret || !imported.RememberPassword || !imported.AutoConnect {
		t.Fatal("portable import did not restore the saved password and automatic connection preference")
	}

	p.AutoConnect = false
	p.RememberPassword = false
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(`"password":`)) ||
		bytes.Contains(data, []byte(`"encryptedPassword":`)) {
		t.Fatal("local profile still contains the password after opt-out")
	}
	if bytes.Contains(data, []byte(`"rememberPassword":`)) {
		t.Fatal("profile without password storage should remain compatible with earlier importers")
	}
	apiProfile, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(apiProfile, []byte(`"rememberPassword":false`)) {
		t.Fatal("profile JSON did not explicitly report opt-out")
	}
}

func TestImportEncryptedPasswordRequiresOptInAndPasswordAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	p := defaultProfile()
	p.Host, p.User, p.Auth = "server.example.com", "operator", tunnel.AuthPassword
	p.RememberPassword, p.SavedPassword = true, "fixture-password"
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	data, _ := readEncryptedProfile(t, path)
	for _, test := range []struct {
		name   string
		mutate func(*profileFile)
	}{
		{"remember password missing", func(f *profileFile) { f.RememberPassword = false }},
		{"agent auth", func(f *profileFile) { f.Auth = tunnel.AuthAgent }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var disk profileFile
			if err := json.Unmarshal(data, &disk); err != nil {
				t.Fatal(err)
			}
			test.mutate(&disk)
			mutated, err := json.Marshal(disk)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, mutated, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readProfile(path, true); err == nil || strings.Contains(err.Error(), p.SavedPassword) {
				t.Fatal("import accepted encrypted credentials without both opt-in and password auth")
			}
		})
	}
}

func readEncryptedProfile(t *testing.T, path string) ([]byte, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		EncryptedPassword string `json:"encryptedPassword"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	encrypted, err := base64.StdEncoding.DecodeString(stored.EncryptedPassword)
	if err != nil {
		t.Fatal(err)
	}
	return data, encrypted
}

func TestUnreadablePasswordDoesNotBlockTheProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	p := defaultProfile()
	p.Host, p.User, p.Auth = "server.example.com", "operator", tunnel.AuthPassword
	p.AutoConnect, p.RememberPassword = true, true
	p.SavedPassword = "fixture-password"
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	data, sealed := readEncryptedProfile(t, path)
	sealed[len(sealed)-1] ^= 1
	damaged, err := json.Marshal(base64.StdEncoding.EncodeToString(sealed))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		tamper func(*profileFile)
	}{
		{"host", func(f *profileFile) { f.Host = "another.example.com" }},
		{"port", func(f *profileFile) { f.SSHPort++ }},
		{"user", func(f *profileFile) { f.User = "different-user" }},
		{"tag", func(f *profileFile) { f.EncryptedPassword = damaged }},
		{"ciphertext type", func(f *profileFile) { f.EncryptedPassword = json.RawMessage(`{}`) }},
		{"ciphertext missing", func(f *profileFile) { f.EncryptedPassword = nil }},
		{"old plaintext", func(f *profileFile) {
			f.EncryptedPassword = nil
			f.LegacyPassword = json.RawMessage(`"fixture-password"`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var disk profileFile
			if err := json.Unmarshal(data, &disk); err != nil {
				t.Fatal(err)
			}
			test.tamper(&disk)
			changed, err := json.Marshal(disk)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadProfile(path)
			if err != nil || loaded.SavedPassword != "" || !loaded.SavedPasswordUnavailable ||
				!loaded.RememberPassword || loaded.AutoConnect {
				t.Fatalf("unreadable credential blocked normal recovery: %v", err)
			}
			if loaded.Host != disk.Host || loaded.User != disk.User || loaded.SSHPort != disk.SSHPort ||
				len(loaded.Forwards) != 1 || loaded.Forwards[0] != p.Forwards[0] {
				t.Fatal("unreadable credential changed the nonsecret configuration")
			}
			if _, err := readProfile(path, true); err == nil || strings.Contains(err.Error(), p.SavedPassword) {
				t.Fatal("portable import accepted an unreadable credential or exposed the password")
			}
		})
	}
}

func TestPasswordAutoConnectRequiresOptInAndStoredPassword(t *testing.T) {
	p := defaultProfile()
	p.Auth = tunnel.AuthPassword
	p.AutoConnect = true
	p.SavedPassword = "fixture-password"
	if err := p.validateStorage(); err == nil {
		t.Fatal("automatic connection accepted a password without consent to store it")
	}
	p.RememberPassword = true
	p.SavedPassword = ""
	if err := p.validateStorage(); err == nil {
		t.Fatal("automatic connection accepted an empty saved password")
	}
}

func TestReadProfileDropsUnapprovedPasswordStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	const imported = `{
	  "version":2, "auth":"password", "rememberPassword":true,
	  "forwards":[{"remotePort":9000,"localPort":4000}]
	}`
	if err := os.WriteFile(path, []byte(imported), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := readProfile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.RememberPassword || p.SavedPassword != "" {
		t.Fatal("import carried an opt-in to password storage")
	}
	const noPasswordAuth = `{
	  "version":2, "auth":"agent", "rememberPassword":true, "password":"fixture-password",
	  "forwards":[{"remotePort":9000,"localPort":4000}]
	}`
	if err := os.WriteFile(path, []byte(noPasswordAuth), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProfile(path, true); err == nil || strings.Contains(err.Error(), "fixture-password") {
		t.Fatal("import should reject legacy plaintext without exposing it")
	}
	p, err = loadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.RememberPassword || p.SavedPassword != "" {
		t.Fatal("local load kept a password for agent authentication")
	}
}
