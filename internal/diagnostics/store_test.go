package diagnostics

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"reversessh/internal/tunnel"
)

func TestRetentionAndRestart(t *testing.T) {
	dir := t.TempDir()
	startup := filepath.Join(dir, "startup.log")
	writeFixture(t, startup, strings.Repeat("startup\n", 45*1024))
	store := New(dir)
	t.Cleanup(store.Close)
	if stats := store.Stats(); stats.MaxMiB != defaultMaxMiB || stats.Bytes == 0 || stats.Error != "" {
		t.Fatalf("unexpected default settings: %+v", stats)
	}
	for i := 0; i < 240; i++ {
		store.Record(tunnel.Event{
			Kind: "retry", Message: fmt.Sprintf("event %03d %s", i, strings.Repeat("x", 7*1024)), Status: i%2 == 0,
		})
	}
	if before := store.Stats(); before.Bytes <= mib {
		t.Fatalf("fixture did not produce enough logs for retention: %+v", before)
	}
	if err := store.SetLimit(1); err != nil {
		t.Fatal(err)
	}
	if stats := store.Stats(); stats.Bytes <= 0 || stats.Bytes > mib || stats.MaxMiB != 1 || stats.Error != "" {
		t.Fatalf("retention cap not applied: %+v", stats)
	}
	if info, err := os.Stat(startup); err != nil || info.Size() != 0 {
		t.Fatalf("old startup log was not truncated in place: %v, %v", info, err)
	}
	for i := 240; i < 350; i++ {
		store.Record(tunnel.Event{
			Kind: "retry", Message: fmt.Sprintf("event %03d %s", i, strings.Repeat("x", 7*1024)), Status: i%2 == 0,
		})
	}
	if stats := store.Stats(); stats.Bytes > mib || stats.Error != "" {
		t.Fatalf("new writes exceeded the configured cap: %+v", stats)
	}
	want := store.Recent()
	if len(want) != recentLimit || !strings.HasPrefix(want[0].Message, "event 286 ") ||
		!strings.HasPrefix(want[len(want)-1].Message, "event 348 ") {
		t.Fatalf("unexpected recent entries count: %d", len(want))
	}
	store.Close()
	store.Close()

	reopened := New(dir)
	t.Cleanup(reopened.Close)
	if stats := reopened.Stats(); stats.Bytes <= 0 || stats.Bytes > mib || stats.MaxMiB != 1 || stats.Error != "" {
		t.Fatalf("retention setting did not survive restart: %+v", stats)
	}
	got := reopened.Recent()
	if !slices.EqualFunc(got, want, func(a, b tunnel.Event) bool {
		a.Time, b.Time = a.Time.UTC(), b.Time.UTC()
		return a == b
	}) {
		t.Fatalf("recent status events did not survive restart: got %d, want %d", len(got), len(want))
	}
}

func TestExportAndClearOnlyRecognizedLogs(t *testing.T) {
	dir := t.TempDir()
	fallback := filepath.Join(t.TempDir(), "startup.log")
	standard := filepath.Join(dir, "startup.log")
	writeFixture(t, standard, "standard startup\n")
	writeFixture(t, fallback, "fallback startup\n")
	writeFixture(t, filepath.Join(dir, "profile.json"), "not a log: profile secret")
	writeFixture(t, filepath.Join(dir, "identity.pem"), "not a log: private key")
	handle, err := os.OpenFile(standard, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	store := New(dir, standard, fallback)
	t.Cleanup(store.Close)
	if err := store.SetLimit(4); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "identity.pem"), filepath.Join(store.dir, "events-00000000000000000000-3.jsonl")); err != nil && !os.IsPermission(err) {
		t.Logf("symlinks unavailable: %v", err)
	}
	store.Record(tunnel.Event{Kind: "noise", Message: "detailed message"})
	store.Record(tunnel.Event{Kind: "retry", Status: true, Message: "visible status"})
	output := filepath.Join(t.TempDir(), "logs.zip")
	if err := store.Export(output); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	var contents strings.Builder
	if len(archive.File) != 3 {
		t.Errorf("archive contains %d entries, want 3", len(archive.File))
	}
	for _, item := range archive.File {
		if item.Name != "startup.log" && item.Name != "startup-2.log" && !eventFilename.MatchString(item.Name) {
			t.Errorf("unexpected ZIP member %q", item.Name)
		}
		entry, err := item.Open()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(&contents, entry); err != nil {
			t.Fatal(err)
		}
		entry.Close()
	}
	archive.Close()
	for _, sample := range []string{"standard startup", "fallback startup", "detailed message", "visible status"} {
		if !strings.Contains(contents.String(), sample) {
			t.Errorf("archive is missing %q", sample)
		}
	}
	if strings.Contains(contents.String(), "not a log") {
		t.Fatal("archive contains private or unrelated files")
	}
	if err := store.Export(filepath.Join(dir, "overwrite.zip")); err == nil {
		t.Fatal("allowed export to data directory")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err == nil && store.Export(filepath.Join(alias, "overwrite.zip")) == nil {
		t.Fatal("allowed export to data directory through a symlink")
	}
	if err := store.Export(fallback); err == nil {
		t.Fatal("allowed export to external startup log")
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if stats := store.Stats(); stats.Bytes != 0 || stats.MaxMiB != 4 || stats.Error != "" || len(store.Recent()) != 0 {
		t.Fatalf("unexpected stats after clearing: %+v", stats)
	}
	if _, err := handle.WriteString("continued append\n"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(standard); err != nil || string(data) != "continued append\n" {
		t.Fatalf("clearing disrupted the live startup writer: %q, %v", data, err)
	}
	if contents, err := os.ReadFile(filepath.Join(dir, "profile.json")); err != nil || string(contents) != "not a log: profile secret" {
		t.Fatalf("clearing altered the profile: %q, %v", contents, err)
	}
}

func TestConcurrentRecordExportAndClear(t *testing.T) {
	store := New(t.TempDir())
	t.Cleanup(store.Close)
	outputs := t.TempDir()
	var tasks sync.WaitGroup
	for worker := range 3 {
		tasks.Go(func() {
			for range 12 {
				store.Record(tunnel.Event{Kind: "retry", Message: "fixture", Status: true})
				output := filepath.Join(outputs, fmt.Sprintf("logs-%d.zip", worker))
				if err := store.Export(output); err != nil {
					t.Error(err)
				}
				if err := store.Clear(); err != nil {
					t.Error(err)
				}
			}
		})
	}
	tasks.Wait()
	if stats := store.Stats(); stats.Bytes != 0 || stats.Error != "" {
		t.Fatalf("unexpected diagnostic state after concurrent clear: %+v", stats)
	}
}

func writeFixture(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
