//go:build !darwin && !windows

package startup

import "errors"

func Enabled() (bool, error) {
	return false, nil
}

func SetEnabled(bool) error {
	return errors.New("仅在 macOS 和 Windows 上支持开机启动")
}
