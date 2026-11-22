package startup

import (
	"errors"
	"os"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey        = `Software\Microsoft\Windows\CurrentVersion\Run`
	appRegistryID = "Reverse SSH"
)

// Enabled reports whether the current executable is registered for login.
func Enabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	installed, _, err := key.GetStringValue(appRegistryID)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	command, err := loginCommand()
	if err != nil {
		return false, err
	}
	return strings.EqualFold(installed, command), nil
}

// SetEnabled sets whether the current executable is registered for login.
func SetEnabled(enabled bool) error {
	if !enabled {
		key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		err = key.DeleteValue(appRegistryID)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}

	command, err := loginCommand()
	if err != nil {
		return err
	}
	// Windows limits Run entries to a 260-character command line.
	if len(utf16.Encode([]rune(command))) > 260 {
		return errors.New("开机启动路径过长，请将程序移到路径更短的文件夹")
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(appRegistryID, command)
}

func loginCommand() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Windows filenames cannot contain double quotes.
	return `"` + executable + `" ` + loginArgument, nil
}
