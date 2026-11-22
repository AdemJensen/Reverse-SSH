package desktop

import (
	"log"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

const (
	wmTrayNotify      = 0x8003
	wmContextMenu     = 0x007B
	wmLeftDoubleClick = 0x0203
	wmRightButtonUp   = 0x0205
	trayMouseSelect   = 0x0400 // NIN_SELECT
	trayKeySelect     = 0x0401 // NIN_KEYSELECT
	trayIconID        = 1
	trayShow          = 1
	trayQuit          = 2
	trayRetryTimer    = 1
	trayRetryInterval = 5000 // milliseconds

	trayAdd        = 0
	trayModify     = 1
	trayDelete     = 2
	traySetVersion = 4
	trayVersion    = 4    // NOTIFYICON_VERSION_4
	trayFlags      = 0x87 // NIF_MESSAGE | NIF_ICON | NIF_TIP | NIF_SHOWTIP
)

var (
	registerWindowMessage = user32.NewProc("RegisterWindowMessageW")
	setTimer              = user32.NewProc("SetTimer")
	killTimer             = user32.NewProc("KillTimer")
	createPopupMenu       = user32.NewProc("CreatePopupMenu")
	destroyMenu           = user32.NewProc("DestroyMenu")
	appendMenu            = user32.NewProc("AppendMenuW")
	trackPopupMenu        = user32.NewProc("TrackPopupMenuEx")
	getCursorPos          = user32.NewProc("GetCursorPos")
	shell32               = windows.NewLazySystemDLL("shell32.dll")
	notifyIcon            = shell32.NewProc("Shell_NotifyIconW")
	notifyIconGetRect     = shell32.NewProc("Shell_NotifyIconGetRect")
)

type notifyIconData struct {
	size                uint32
	window              uintptr
	id, flags, callback uint32
	icon                uintptr
	tip                 [128]uint16
	state, stateMask    uint32
	info                [256]uint16
	version             uint32
	infoTitle           [64]uint16
	infoFlags           uint32
	guid                windows.GUID
	balloonIcon         uintptr
}

type notifyIconIdentifier struct {
	size   uint32
	window uintptr
	id     uint32
	guid   windows.GUID
}

func (shell *windowsShell) registerTrayIcon() {
	if shell.trayAdded || shell.addTrayIcon() {
		killTimer.Call(shell.handle, trayRetryTimer)
		log.Print("Windows 系统托盘图标已注册")
		return
	}
	// Explorer may still be starting when the login entry runs.
	setTimer.Call(shell.handle, trayRetryTimer, trayRetryInterval, 0)
}

func (shell *windowsShell) addTrayIcon() bool {
	if shell.handle == 0 {
		return false
	}
	data := notifyIconData{
		size: uint32(unsafe.Sizeof(notifyIconData{})), window: shell.handle, id: trayIconID,
		flags: trayFlags, callback: wmTrayNotify, icon: shell.trayIcon, version: trayVersion,
	}
	copy(data.tip[:], utf16.Encode([]rune(windowTitle)))
	added, _, _ := notifyIcon.Call(trayAdd, uintptr(unsafe.Pointer(&data)))
	if added == 0 {
		added, _, _ = notifyIcon.Call(trayModify, uintptr(unsafe.Pointer(&data)))
	}
	shell.trayAdded = added != 0
	if !shell.trayAdded {
		return false
	}
	version, _, _ := notifyIcon.Call(traySetVersion, uintptr(unsafe.Pointer(&data)))
	shell.trayV4 = version != 0
	return true
}

func (shell *windowsShell) removeTrayIcon() {
	if !shell.trayAdded {
		return
	}
	data := notifyIconData{size: uint32(unsafe.Sizeof(notifyIconData{})), window: shell.handle, id: trayIconID}
	notifyIcon.Call(trayDelete, uintptr(unsafe.Pointer(&data)))
	shell.trayAdded = false
}

func (shell *windowsShell) trayAction(parameter uintptr) {
	switch uint16(parameter) { // NOTIFYICON_VERSION_4 encodes the icon ID in the high word.
	case trayMouseSelect, trayKeySelect, wmLeftDoubleClick:
		shell.openWindow()
	case wmContextMenu:
		shell.showTrayMenu()
	case wmRightButtonUp:
		if !shell.trayV4 {
			shell.showTrayMenu()
		}
	}
}

func (shell *windowsShell) showTrayMenu() {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	defer destroyMenu.Call(menu)
	show := windows.StringToUTF16Ptr("打开窗口")
	quit := windows.StringToUTF16Ptr("退出 SSH 反向隧道")
	appendMenu.Call(menu, 0, trayShow, uintptr(unsafe.Pointer(show)))
	appendMenu.Call(menu, 0x0800, 0, 0) // MF_SEPARATOR
	appendMenu.Call(menu, 0, trayQuit, uintptr(unsafe.Pointer(quit)))

	position := shell.trayMenuPosition()
	setForegroundWindow.Call(shell.handle)
	const menuFlags = 0x0182 // TPM_RETURNCMD | TPM_NONOTIFY | TPM_RIGHTBUTTON
	command, _, _ := trackPopupMenu.Call(menu, menuFlags, uintptr(position.X), uintptr(position.Y), shell.handle, 0)
	postMessage.Call(shell.handle, 0, 0, 0) // WM_NULL lets the shell dismiss the next popup normally.
	switch command {
	case trayShow:
		shell.openWindow()
	case trayQuit:
		shell.quit()
	}
}

func (shell *windowsShell) trayMenuPosition() windowPoint {
	icon := notifyIconIdentifier{size: uint32(unsafe.Sizeof(notifyIconIdentifier{})), window: shell.handle, id: trayIconID}
	var bounds edge.Rect
	result, _, _ := notifyIconGetRect.Call(uintptr(unsafe.Pointer(&icon)), uintptr(unsafe.Pointer(&bounds)))
	if int32(result) >= 0 {
		return windowPoint{X: bounds.Left, Y: bounds.Top}
	}
	var cursor windowPoint
	getCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
	return cursor
}
