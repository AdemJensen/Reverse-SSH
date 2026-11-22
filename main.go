package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"reversessh/internal/control"
	"reversessh/internal/desktop"
	"reversessh/internal/ui"
)

func init() {
	runtime.LockOSThread()
}

func main() {
	autostart := flag.Bool("autostart", false, "app opened by the current user's login launcher")
	flag.Parse()
	logPath, closeDiagnostics := startDiagnostics(*autostart)
	defer closeDiagnostics()
	if err := run(*autostart); err != nil {
		if logPath != "" {
			log.Printf("启动失败：%v", err)
		}
		if *autostart && logPath == "" {
			fmt.Fprintln(os.Stderr, err)
		}
		if !*autostart {
			message := err.Error()
			if logPath != "" {
				message += "\n\n启动日志位于\n" + logPath
			}
			desktop.Alert(message)
		}
		closeDiagnostics()
		os.Exit(1)
	}
}

func run(autostart bool) error {
	base, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("无法定位用户配置目录：%w", err)
	}
	dataDir := filepath.Join(base, "Reverse SSH")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("无法创建用户配置目录：%w", err)
	}
	unlock, err := lockInstance(filepath.Join(dataDir, "instance.lock"))
	if errors.Is(err, errAlreadyRunning) {
		if runtime.GOOS == "windows" {
			log.Print("检测到已有实例。")
		}
		if !autostart && !desktop.ReopenExisting() {
			desktop.Alert("SSH 反向隧道已在运行，请切换到现有窗口。")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("无法锁定用户配置目录：%w", err)
	}
	defer unlock()
	controller, err := control.New(dataDir, autostart, control.Dialogs{
		PickKey: desktop.PickKey, Import: desktop.PickImportConfig, Export: desktop.PickExportConfig,
		OpenURL: desktop.OpenURL,
	})
	if err != nil {
		return err
	}
	defer controller.Close()
	server, url, err := controller.Listen(ui.Assets)
	if err != nil {
		return fmt.Errorf("无法启动本机界面：%w", err)
	}
	defer server.Close()
	if runtime.GOOS == "windows" {
		log.Print("本机界面服务已就绪。")
	}
	return desktop.Run(url, dataDir, autostart)
}

var errAlreadyRunning = errors.New("already running")
