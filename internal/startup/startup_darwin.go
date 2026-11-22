package startup

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const launchAgentLabel = "com.reversessh.desktop"

// Enabled reports whether the current executable is registered for login.
func Enabled() (bool, error) {
	path, err := launchAgentPath()
	if err != nil {
		return false, err
	}
	installed, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	return bytes.Equal(installed, launchAgent(executable)), nil
}

// SetEnabled sets whether the current executable is registered for login.
func SetEnabled(enabled bool) error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if !enabled {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.Contains(executable, "/AppTranslocation/") {
		return errors.New("请将应用移到“应用程序”文件夹，重新打开后再开启登录时启动")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Write beside the finished plist so launchd only sees complete contents.
	temp, err := os.CreateTemp(dir, launchAgentLabel+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(launchAgent(executable))
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}

func launchAgent(executable string) []byte {
	var escapedPath bytes.Buffer
	_ = xml.EscapeText(&escapedPath, []byte(executable))
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>LimitLoadToSessionType</key>
    <string>Aqua</string>
</dict>
</plist>
`, launchAgentLabel, escapedPath.String(), loginArgument)
}
