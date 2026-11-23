package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
)

func startDiagnostics(autostart bool) (string, func()) {
	configDir, _ := os.UserConfigDir()
	for _, base := range []string{configDir, os.TempDir()} {
		if base == "" {
			continue
		}
		dir := filepath.Join(base, "Reverse SSH")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		path := filepath.Join(dir, "startup.log")
		if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
			continue
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			continue
		}
		if info, err := file.Stat(); err == nil && info.Size() > 128<<10 {
			_ = file.Truncate(0)
		}
		previousOutput := log.Writer()
		log.SetOutput(file)
		if err := debug.SetCrashOutput(file, debug.CrashOptions{}); err != nil {
			log.Printf("无法设置崩溃日志：%v", err)
		}
		log.Printf("Reverse SSH 启动，平台 %s/%s，登录启动 %t。", runtime.GOOS, runtime.GOARCH, autostart)
		return path, func() {
			_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
			log.SetOutput(previousOutput)
			_ = file.Close()
		}
	}
	return "", func() {}
}
