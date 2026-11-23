// Package diagnostics stores the application's bounded, exportable local logs.
package diagnostics

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"reversessh/internal/tunnel"
)

const (
	defaultMaxMiB = 16
	mib           = 1024 * 1024
	segmentBytes  = 256 * 1024
	recentLimit   = 32
	trimInterval  = 30 * time.Second
)

var eventFilename = regexp.MustCompile(`^events-[0-9]{20}-[0-9]+\.jsonl$`)

// Stats reports the bytes used by logs and the configured storage limit.
type Stats struct {
	Bytes  int64  `json:"bytes"`
	MaxMiB int    `json:"maxMiB"`
	Error  string `json:"error,omitempty"`
}

type logFile struct {
	path    string
	name    string
	info    os.FileInfo
	startup bool
}

type storedEvent struct {
	event   tunnel.Event
	segment string
}

// Store serializes log writes, rotation and settings changes.
type Store struct {
	mu           sync.Mutex
	exportMu     sync.Mutex
	dataDir      string
	dir          string
	startupPaths []string
	maxMiB       int
	bytes        int64
	lastError    error
	active       string
	activeBytes  int64
	recent       []storedEvent
	stop         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
}

// New includes the normal startup log and any explicit startup-log paths used by the app.
func New(dataDir string, startupPaths ...string) *Store {
	normalPath := filepath.Join(dataDir, "startup.log")
	s := &Store{
		dataDir: dataDir, dir: filepath.Join(dataDir, "diagnostics"), maxMiB: defaultMaxMiB,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	for _, path := range append([]string{normalPath}, startupPaths...) {
		if !strings.EqualFold(filepath.Base(path), "startup.log") {
			continue
		}
		var err error
		path, err = filepath.Abs(path)
		if err != nil {
			s.lastError = err
			continue
		}
		if !containsPath(s.startupPaths, path) {
			s.startupPaths = append(s.startupPaths, path)
		}
	}
	if err := ensureDirectory(s.dir); err != nil {
		s.lastError = err
	} else {
		s.lastError = errors.Join(s.lastError, s.loadLimit(), s.trim(), s.restoreRecent())
	}
	go s.maintain()
	return s
}

func containsPath(paths []string, path string) bool {
	for _, candidate := range paths {
		if strings.EqualFold(path, candidate) {
			return true
		}
	}
	return false
}

func ensureDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("日志目录不是普通目录：%s", path)
	}
	return nil
}

func regularInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return nil, nil
	}
	return info, err
}

func openLog(path string, flags int, expected os.FileInfo) (*os.File, error) {
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !os.SameFile(info, expected) {
		err = fmt.Errorf("日志文件已更改：%s", path)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (s *Store) loadLimit() error {
	path := filepath.Join(s.dir, "settings.json")
	info, err := regularInfo(path)
	if err != nil || info == nil {
		return err
	}
	file, err := openLog(path, os.O_RDONLY, info)
	if err != nil {
		return err
	}
	defer file.Close()
	var settings struct {
		MaxMiB int `json:"maxMiB"`
	}
	if err := json.NewDecoder(io.LimitReader(file, 4096)).Decode(&settings); err != nil {
		return err
	}
	if err := validateLimit(settings.MaxMiB); err != nil {
		return err
	}
	s.maxMiB = settings.MaxMiB
	return nil
}

func validateLimit(maxMiB int) error {
	if maxMiB < 1 || maxMiB > 256 {
		return errors.New("日志大小上限必须为 1 到 256 MiB")
	}
	return nil
}

// SetLimit saves a new cap and immediately applies it to existing logs.
func (s *Store) SetLimit(maxMiB int) error {
	if err := validateLimit(maxMiB); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureDirectory(s.dir); err != nil {
		s.lastError = err
		return err
	}
	path := filepath.Join(s.dir, "settings.json")
	err := writeAtomic(path, func(file *os.File) error {
		return json.NewEncoder(file).Encode(struct {
			MaxMiB int `json:"maxMiB"`
		}{maxMiB})
	})
	if err == nil {
		s.maxMiB = maxMiB
		err = s.trim()
	}
	s.lastError = err
	return err
}

// Record appends a single event and updates the recent user-visible status list.
func (s *Store) Record(event tunnel.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	event.Kind = clip(event.Kind, 128)
	event.Level = clip(event.Level, 32)
	event.State = clip(event.State, 64)
	event.Message = clip(event.Message, 8*1024)
	data, err := json.Marshal(event)
	if err != nil {
		s.lastError = err
		return
	}
	data = append(data, '\n')
	var file *os.File
	rotated := s.active == "" || s.activeBytes+int64(len(data)) > segmentBytes
	if rotated {
		if err = ensureDirectory(s.dir); err == nil {
			pattern := fmt.Sprintf("events-%020d-*.jsonl", time.Now().UnixNano())
			file, err = os.CreateTemp(s.dir, pattern)
		}
		if err == nil {
			s.active, s.activeBytes = file.Name(), 0
		}
	} else {
		var info os.FileInfo
		info, err = regularInfo(s.active)
		if err == nil {
			if info != nil {
				file, err = openLog(s.active, os.O_WRONLY|os.O_APPEND, info)
			} else {
				err = fmt.Errorf("日志文件不可用：%s", s.active)
				s.active, s.activeBytes = "", 0
			}
		}
	}
	if err == nil {
		var n int
		n, err = file.Write(data)
		err = errors.Join(err, file.Close())
		s.activeBytes += int64(n)
		s.bytes += int64(n)
	}
	if event.Status {
		s.addRecent(storedEvent{event: event, segment: s.active})
	}
	if err == nil && (rotated || s.bytes > int64(s.maxMiB)*mib) {
		err = s.trim()
	}
	if err != nil {
		s.lastError = err
	}
}

func clip(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return strings.ToValidUTF8(value[:maxBytes], "")
}

func (s *Store) addRecent(event storedEvent) {
	if len(s.recent) == recentLimit {
		copy(s.recent, s.recent[1:])
		s.recent = s.recent[:recentLimit-1]
	}
	s.recent = append(s.recent, event)
}

// Recent returns a copy of the last 32 retained status events in time order.
func (s *Store) Recent() []tunnel.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]tunnel.Event, 0, len(s.recent))
	for _, item := range s.recent {
		events = append(events, item.event)
	}
	return events
}

func (s *Store) restoreRecent() error {
	files, err := s.files()
	if err != nil {
		return err
	}
	var newest []storedEvent
	for idx := len(files) - 1; idx >= 0 && len(newest) < recentLimit; idx-- {
		item := files[idx]
		if item.startup {
			continue
		}
		file, err := openLog(item.path, os.O_RDONLY, item.info)
		if err != nil {
			return err
		}
		var inSegment []storedEvent
		scanner := bufio.NewScanner(io.LimitReader(file, segmentBytes))
		for scanner.Scan() {
			var event tunnel.Event
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Status {
				inSegment = append(inSegment, storedEvent{event: event, segment: item.path})
			}
		}
		if err = errors.Join(scanner.Err(), file.Close()); err != nil {
			return err
		}
		for i := len(inSegment) - 1; i >= 0 && len(newest) < recentLimit; i-- {
			newest = append(newest, inSegment[i])
		}
	}
	for i := len(newest) - 1; i >= 0; i-- {
		s.addRecent(newest[i])
	}
	return nil
}

func (s *Store) files() ([]logFile, error) {
	if err := ensureDirectory(s.dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var files []logFile
	for _, entry := range entries {
		if eventFilename.MatchString(entry.Name()) {
			files, err = addLog(files, filepath.Join(s.dir, entry.Name()), entry.Name(), false)
			if err != nil {
				return nil, err
			}
		}
	}
	for i, path := range s.startupPaths {
		name := "startup.log"
		if i > 0 {
			name = fmt.Sprintf("startup-%d.log", i+1)
		}
		files, err = addLog(files, path, name, true)
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if a.info.ModTime().Equal(b.info.ModTime()) {
			return a.name < b.name
		}
		return a.info.ModTime().Before(b.info.ModTime())
	})
	return files, nil
}

func addLog(files []logFile, path, name string, startup bool) ([]logFile, error) {
	info, err := regularInfo(path)
	if err != nil || info == nil {
		return files, err
	}
	return append(files, logFile{path: path, name: name, info: info, startup: startup}), nil
}

func fileBytes(files []logFile) int64 {
	var size int64
	for _, item := range files {
		size += item.info.Size()
	}
	return size
}

// Stats includes externally appended startup-log bytes.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if files, err := s.files(); err != nil {
		s.lastError = err
	} else {
		s.bytes = fileBytes(files)
	}
	result := Stats{Bytes: s.bytes, MaxMiB: s.maxMiB}
	if s.lastError != nil {
		result.Error = s.lastError.Error()
	}
	return result
}

func (s *Store) trim() error {
	files, err := s.files()
	if err != nil {
		return err
	}
	s.bytes = fileBytes(files)
	for _, item := range files {
		if s.bytes <= int64(s.maxMiB)*mib {
			break
		}
		if item.info.Size() == 0 {
			continue
		}
		if err = s.remove(item); err != nil {
			return err
		}
		s.bytes -= item.info.Size()
	}
	return nil
}

func (s *Store) remove(item logFile) error {
	if item.startup {
		file, err := openLog(item.path, os.O_WRONLY, item.info)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return errors.Join(file.Truncate(0), file.Close())
	}
	if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.active == item.path {
		s.active, s.activeBytes = "", 0
	}
	retained := s.recent[:0]
	for _, event := range s.recent {
		if event.segment != item.path {
			retained = append(retained, event)
		}
	}
	s.recent = retained
	return nil
}

// Clear removes logs and status history, preserving the configured limit.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.files()
	if err == nil {
		for _, item := range files {
			err = errors.Join(err, s.remove(item))
		}
	}
	if err == nil {
		s.bytes = 0
		s.recent = nil
	}
	s.lastError = err
	return err
}

func (s *Store) maintain() {
	defer close(s.done)
	ticker := time.NewTicker(trimInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			if err := s.trim(); err != nil {
				s.lastError = err
			}
			s.mu.Unlock()
		case <-s.stop:
			return
		}
	}
}

// Close stops and waits for periodic maintenance.
func (s *Store) Close() {
	s.closeOnce.Do(func() { close(s.stop) })
	<-s.done
}
