//go:build darwin || windows

package desktop

import (
	"errors"
	"fmt"
	"net/url"
)

// OpenURL opens an HTTP or HTTPS address in the system's default browser.
func OpenURL(address string) error {
	target, err := url.Parse(address)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil {
		return errors.New("只能在浏览器中打开完整的 HTTP 或 HTTPS 地址")
	}
	if err := openBrowser(target.String()); err != nil {
		return fmt.Errorf("无法打开默认浏览器：%w", err)
	}
	return nil
}
