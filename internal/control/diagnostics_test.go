package control

import (
	"archive/zip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reversessh/internal/tunnel"
)

func TestDiagnosticsAPI(t *testing.T) {
	dir := t.TempDir()
	exportPath := filepath.Join(t.TempDir(), "diagnostics.zip")
	startupPath := filepath.Join(dir, "startup.log")
	startupLog, err := os.OpenFile(startupPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer startupLog.Close()
	if _, err := startupLog.WriteString("startup fixture\n"); err != nil {
		t.Fatal(err)
	}
	cancelExport := true
	c, err := New(dir, false, Dialogs{
		ExportLogs: func(startDir, name string) (string, error) {
			if startDir != "" || !strings.HasPrefix(name, "ReverseSSH-Logs-") || !strings.HasSuffix(name, ".zip") {
				t.Errorf("unexpected save dialog defaults: %q, %q", startDir, name)
			}
			if cancelExport {
				return "", nil
			}
			return exportPath, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)

	c.diagnostics.Record(tunnel.Event{Time: time.Now(), Kind: "ssh_attempt", Level: "info", Message: "detailed fixture event"})
	c.diagnostics.Record(tunnel.Event{
		Time: time.Now(), Kind: "connection_failed", Level: "error", State: tunnel.StateError,
		Message: "status fixture event", Status: true,
	})

	var result exportResult
	body := controllerJSON(t, c, http.MethodPost, "api/logs/export", struct{}{}, http.StatusOK)
	if err := json.Unmarshal(body, &result); err != nil || result.Saved {
		t.Fatalf("cancel did not leave export untouched: %s, %v", body, err)
	}
	if _, err := os.Stat(exportPath); !os.IsNotExist(err) {
		t.Fatalf("cancelled export created a file: %v", err)
	}
	cancelExport = false
	body = controllerJSON(t, c, http.MethodPost, "api/logs/export", struct{}{}, http.StatusOK)
	if err := json.Unmarshal(body, &result); err != nil || !result.Saved {
		t.Fatalf("export did not report success: %s, %v", body, err)
	}
	archive, err := zip.OpenReader(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var content strings.Builder
	for _, file := range archive.File {
		entry, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(&content, entry)
		entry.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []string{"startup fixture", "detailed fixture event", "status fixture event"} {
		if !strings.Contains(content.String(), fixture) {
			t.Fatalf("archive is missing %q", fixture)
		}
	}

	controllerJSON(t, c, http.MethodPut, "api/logs/limit", map[string]int{"maxMiB": 0}, http.StatusBadRequest)
	body = controllerJSON(t, c, http.MethodPut, "api/logs/limit", map[string]int{"maxMiB": 2}, http.StatusOK)
	var state responseState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if state.Diagnostics.MaxMiB != 2 || state.Diagnostics.Bytes == 0 ||
		len(state.Logs) != 1 || state.Logs[0].Level != "error" || state.Logs[0].Message != "status fixture event" {
		t.Fatalf("unexpected state after setting a limit: %s", body)
	}
	body = controllerJSON(t, c, http.MethodPost, "api/logs/clear", struct{}{}, http.StatusOK)
	state = responseState{}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if state.Diagnostics.MaxMiB != 2 || state.Diagnostics.Bytes != 0 || len(state.Logs) != 0 {
		t.Fatalf("unexpected state after clearing logs: %s", body)
	}
	if _, err := startupLog.WriteString("startup after clear\n"); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(startupPath)
	if err != nil || string(saved) != "startup after clear\n" {
		t.Fatalf("clearing logs disconnected the active startup writer: %q, %v", saved, err)
	}
}
