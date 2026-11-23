package diagnostics

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Export writes only recognized diagnostic logs into an atomically replaced ZIP.
func (s *Store) Export(path string) error {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	path, err := s.exportPath(path)
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "reversessh-logs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	names, err := s.snapshot(scratch)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(scratch)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeAtomic(path, func(file *os.File) error {
		archive := zip.NewWriter(file)
		for _, name := range names {
			if err := addToArchive(archive, root, name); err != nil {
				archive.Close()
				return err
			}
		}
		return archive.Close()
	})
}

func (s *Store) exportPath(path string) (string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".zip") {
		return "", errors.New("请选择 ZIP 日志导出文件")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	dataInfo, err := os.Stat(s.dataDir)
	if err != nil {
		return "", err
	}
	for ancestor := parent; ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Stat(ancestor)
		if err != nil {
			return "", err
		}
		if os.SameFile(info, dataInfo) {
			return "", errors.New("请选择应用配置目录以外的导出位置")
		}
		if next := filepath.Dir(ancestor); next == ancestor {
			break
		}
	}
	path = filepath.Join(parent, filepath.Base(abs))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("请选择普通 ZIP 文件作为导出位置")
	}
	for _, startupPath := range s.startupPaths {
		startup, err := regularInfo(startupPath)
		if err != nil {
			return "", err
		}
		if startup != nil && os.SameFile(info, startup) {
			return "", errors.New("不能覆盖现有启动日志")
		}
	}
	return path, nil
}

func (s *Store) snapshot(dir string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.files()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, item := range files {
		if item.info.Size() == 0 {
			continue
		}
		path := filepath.Join(dir, item.name)
		// Sealed segments are immutable. Link them when both paths are on the same
		// volume, and copy mutable files before releasing the store lock.
		if item.startup || item.path == s.active || !linkLog(item, path) {
			if err := copyLog(item, path); err != nil {
				return nil, err
			}
		}
		names = append(names, item.name)
	}
	return names, nil
}

func linkLog(item logFile, destination string) bool {
	if err := os.Link(item.path, destination); err != nil {
		return false
	}
	info, err := os.Lstat(destination)
	if err == nil && info.Mode().IsRegular() && os.SameFile(info, item.info) {
		return true
	}
	_ = os.Remove(destination)
	return false
}

func copyLog(item logFile, destination string) error {
	from, err := openLog(item.path, os.O_RDONLY, item.info)
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.CopyN(to, from, item.info.Size())
	return errors.Join(err, to.Close())
}

func addToArchive(archive *zip.Writer, root *os.Root, name string) error {
	// On Windows a rooted open shares delete access, allowing the store to
	// clear or rotate the original name while this snapshot link is read.
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	entry, err := archive.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, file)
	return err
}

func writeAtomic(path string, write func(*os.File) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".reversessh-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = write(file)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
