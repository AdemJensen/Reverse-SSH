package desktop

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

const (
	windowTitle       = "SSH 反向隧道"
	windowClassName   = "ReverseSSHDesktopWindow"
	wmDestroy         = 0x0002
	wmMove            = 0x0003
	wmSize            = 0x0005
	wmSetFocus        = 0x0007
	wmClose           = 0x0010
	wmEndSession      = 0x0016
	wmGetMinMaxInfo   = 0x0024
	wmCommand         = 0x0111
	wmTimer           = 0x0113
	wmPickFile        = 0x8001
	wmReopen          = 0x8002
	wmStartWebView    = 0x8004
	wmWebViewResult   = 0x8005
	openBrowserButton = 100
	windowStyle       = 0x02CF0000   // WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN
	defaultPosition   = 0x80000000   // CW_USEDEFAULT
	getWorkArea       = 0x0030       // SPI_GETWORKAREA
	showWindowHide    = 0            // SW_HIDE
	showWindowNormal  = 5            // SW_SHOW
	showWindowRestore = 9            // SW_RESTORE
	fileDialogFlags   = 0x00080000 | // OFN_EXPLORER
		0x00000800 | // OFN_PATHMUSTEXIST
		0x00000008 | 0x00000004 // OFN_NOCHANGEDIR | OFN_HIDEREADONLY
	fileMustExist       = 0x00001000 // OFN_FILEMUSTEXIST
	confirmOverwrite    = 0x00000002 // OFN_OVERWRITEPROMPT
	baseDPI             = 96
	defaultClientWidth  = 1034
	defaultClientHeight = 625
)

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	registerClass          = user32.NewProc("RegisterClassExW")
	unregisterClass        = user32.NewProc("UnregisterClassW")
	createWindow           = user32.NewProc("CreateWindowExW")
	destroyWindow          = user32.NewProc("DestroyWindow")
	showWindow             = user32.NewProc("ShowWindow")
	isIconic               = user32.NewProc("IsIconic")
	findWindow             = user32.NewProc("FindWindowW")
	getWindowProcess       = user32.NewProc("GetWindowThreadProcessId")
	allowSetForeground     = user32.NewProc("AllowSetForegroundWindow")
	defaultWindowProc      = user32.NewProc("DefWindowProcW")
	postMessage            = user32.NewProc("PostMessageW")
	postQuitMessage        = user32.NewProc("PostQuitMessage")
	getMessage             = user32.NewProc("GetMessageW")
	translateMessage       = user32.NewProc("TranslateMessage")
	dispatchMessage        = user32.NewProc("DispatchMessageW")
	loadCursor             = user32.NewProc("LoadCursorW")
	loadIcon               = user32.NewProc("LoadIconW")
	messageBox             = user32.NewProc("MessageBoxW")
	systemParametersInfo   = user32.NewProc("SystemParametersInfoW")
	setProcessDPIAware     = user32.NewProc("SetProcessDPIAware")
	getDpiForSystem        = user32.NewProc("GetDpiForSystem")
	adjustWindowRectForDPI = user32.NewProc("AdjustWindowRectExForDpi")
	adjustWindowRect       = user32.NewProc("AdjustWindowRectEx")
	setForegroundWindow    = user32.NewProc("SetForegroundWindow")
	getClientRect          = user32.NewProc("GetClientRect")
	setWindowText          = user32.NewProc("SetWindowTextW")
	setFocus               = user32.NewProc("SetFocus")
	sendMessage            = user32.NewProc("SendMessageW")
	getStockObject         = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetStockObject")
	ole32                  = windows.NewLazySystemDLL("ole32.dll")
	coInitialize           = ole32.NewProc("CoInitializeEx")
	coUninitialize         = ole32.NewProc("CoUninitialize")
	comdlg32               = windows.NewLazySystemDLL("comdlg32.dll")
	getOpenFileName        = comdlg32.NewProc("GetOpenFileNameW")
	getSaveFileName        = comdlg32.NewProc("GetSaveFileNameW")
	commonDialogError      = comdlg32.NewProc("CommDlgExtendedError")
	activeWindow           atomic.Pointer[windowsShell]
)

type windowClass struct {
	size, style             uint32
	windowProc              uintptr
	classExtra, windowExtra int32
	instance, icon, cursor  uintptr
	background              uintptr
	menuName, className     *uint16
	smallIcon               uintptr
}

type windowMessage struct {
	window         uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	point          windowPoint
	private        uint32
}

type windowPoint struct{ X, Y int32 }

type windowTrackSize struct {
	reserved, maxSize, maxPosition, minTrackSize, maxTrackSize windowPoint
}

type windowsShell struct {
	handle         uintptr
	fileOwner      uintptr
	notice         uintptr
	openInBrowser  uintptr
	origin         *url.URL
	webViewDataDir string
	webView        *edge.ICoreWebView2Controller
	webViewPhase   webViewPhase
	webViewResult  error
	comError       error
	browserOpened  bool
	minWidth       int32
	minHeight      int32
	trayIcon       uintptr
	trayAdded      bool
	trayV4         bool
	taskbarCreated uint32
	requests       chan fileRequest
	done           chan struct{}
	picking        atomic.Bool
}

type fileDialogKind int

const (
	keyDialog fileDialogKind = iota
	importDialog
	exportDialog
	logsDialog
)

type fileRequest struct {
	dir           string
	suggestedName string
	kind          fileDialogKind
	result        chan fileResult
}

type fileResult struct {
	path string
	err  error
}

// Run hosts the window and tray icon until the user quits the application.
func Run(address, dataDir string, startHidden bool) error {
	origin, err := url.Parse(address)
	if err != nil || origin.Scheme != "http" || origin.User != nil || !net.ParseIP(origin.Hostname()).IsLoopback() {
		return errors.New("应用界面的地址必须是本机回环地址")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	shell := &windowsShell{
		origin: origin, webViewDataDir: filepath.Join(dataDir, "WebView2"),
		requests: make(chan fileRequest, 1), done: make(chan struct{}),
	}
	hr, _, _ := coInitialize.Call(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if int32(hr) < 0 {
		shell.comError = fmt.Errorf("无法初始化 WebView2 COM 线程：%w", syscall.Errno(uint32(hr)))
	} else {
		defer coUninitialize.Call()
	}
	setProcessDPIAware.Call()

	handle, cleanup, err := shell.createWindow()
	if err != nil {
		return err
	}
	defer cleanup()
	shell.handle = handle
	shell.fileOwner = handle
	defer shell.closeWebView()
	defer shell.removeTrayIcon()
	log.Print("Windows 桌面窗口已创建")
	activeWindow.Store(shell)
	defer func() {
		activeWindow.Store(nil)
		close(shell.done)
	}()
	shell.registerTrayIcon()
	if !startHidden {
		shell.openWindow()
	}

	var message windowMessage
	for {
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		switch int32(result) {
		case -1:
			return fmt.Errorf("Windows 消息循环出错 (%w)", err)
		case 0:
			return nil
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&message)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (shell *windowsShell) createWindow() (uintptr, func(), error) {
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, nil, &instance); err != nil {
		return 0, nil, err
	}
	className := windows.StringToUTF16Ptr(windowClassName)
	icon, _, _ := loadIcon.Call(uintptr(instance), 1) // app resource if present
	if icon == 0 {
		icon, _, _ = loadIcon.Call(0, 32512) // IDI_APPLICATION
	}
	shell.trayIcon = icon
	taskbarCreated, _, _ := registerWindowMessage.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated"))))
	shell.taskbarCreated = uint32(taskbarCreated)
	cursor, _, _ := loadCursor.Call(0, 32512) // IDC_ARROW
	class := windowClass{
		size: uint32(unsafe.Sizeof(windowClass{})), windowProc: windows.NewCallback(shell.windowProc),
		instance: uintptr(instance), icon: icon, smallIcon: icon, cursor: cursor,
		background: 6, className: className, // COLOR_WINDOW + 1
	}
	if atom, _, err := registerClass.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		return 0, nil, fmt.Errorf("无法注册 Windows 应用窗口 (%w)", err)
	}
	x, y, width, height := shell.startPlacement()
	title := windows.StringToUTF16Ptr(windowTitle)
	handle, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		windowStyle, x, y, width, height, 0, 0, uintptr(instance), 0)
	if handle == 0 {
		unregisterClass.Call(uintptr(unsafe.Pointer(className)), uintptr(instance))
		return 0, nil, fmt.Errorf("无法创建 Windows 应用窗口 (%w)", err)
	}
	shell.createStartupControls(handle, uintptr(instance))
	return handle, func() {
		destroyWindow.Call(handle)
		unregisterClass.Call(uintptr(unsafe.Pointer(className)), uintptr(instance))
	}, nil
}

func (shell *windowsShell) startPlacement() (x, y, width, height uintptr) {
	dpi := uintptr(baseDPI)
	if getDpiForSystem.Find() == nil {
		if systemDPI, _, _ := getDpiForSystem.Call(); systemDPI != 0 {
			dpi = systemDPI
		}
	}
	width, height = defaultWindowSize(dpi)
	shell.minWidth = int32(min(760*dpi/baseDPI, width))
	shell.minHeight = int32(min(650*dpi/baseDPI, height))
	var work edge.Rect
	result, _, _ := systemParametersInfo.Call(getWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0)
	if result == 0 || work.Right <= work.Left || work.Bottom <= work.Top {
		return defaultPosition, defaultPosition, width, height
	}
	availableWidth, availableHeight := work.Right-work.Left, work.Bottom-work.Top
	width, height = min(width, uintptr(availableWidth)), min(height, uintptr(availableHeight))
	shell.minWidth = min(shell.minWidth, availableWidth)
	shell.minHeight = min(shell.minHeight, availableHeight)
	return uintptr(work.Left + (availableWidth-int32(width))/2),
		uintptr(work.Top + (availableHeight-int32(height))/2), width, height
}

func defaultWindowSize(dpi uintptr) (width, height uintptr) {
	client := edge.Rect{
		Right:  int32(defaultClientWidth * dpi / baseDPI),
		Bottom: int32(defaultClientHeight * dpi / baseDPI),
	}
	frame := client
	if adjustWindowRectForDPI.Find() == nil {
		if adjusted, _, _ := adjustWindowRectForDPI.Call(uintptr(unsafe.Pointer(&frame)), windowStyle, 0, 0, dpi); adjusted != 0 {
			return uintptr(frame.Right - frame.Left), uintptr(frame.Bottom - frame.Top)
		}
		frame = client
	}
	if adjusted, _, _ := adjustWindowRect.Call(uintptr(unsafe.Pointer(&frame)), windowStyle, 0, 0); adjusted != 0 {
		return uintptr(frame.Right - frame.Left), uintptr(frame.Bottom - frame.Top)
	}
	return uintptr(client.Right), uintptr(client.Bottom)
}

func (shell *windowsShell) windowProc(handle uintptr, message uint32, wParam, lParam uintptr) uintptr {
	if message != 0 && message == shell.taskbarCreated {
		if shell.handle != 0 {
			shell.trayAdded = false
			shell.registerTrayIcon()
		}
		return 0
	}
	switch message {
	case wmGetMinMaxInfo:
		// Windows passes a writable MINMAXINFO pointer as LPARAM for this message.
		track := (*windowTrackSize)(unsafe.Pointer(lParam))
		track.minTrackSize = windowPoint{X: shell.minWidth, Y: shell.minHeight}
		return 0
	case wmSize:
		if shell.webView != nil {
			shell.resizeWebView()
		}
	case wmMove:
		if shell.webView != nil {
			_ = shell.webView.NotifyParentWindowPositionChanged()
		}
	case wmSetFocus:
		if shell.webViewPhase == webViewReady && shell.webView != nil {
			_ = shell.webView.MoveFocus(0)
		} else if shell.openInBrowser != 0 {
			setFocus.Call(shell.openInBrowser)
		}
	case wmCommand:
		if uint16(wParam) == openBrowserButton {
			shell.launchBrowser()
			return 0
		}
	case wmPickFile:
		select {
		case request := <-shell.requests:
			path, err := openFileDialog(handle, request)
			request.result <- fileResult{path, err}
		default:
		}
		return 0
	case wmReopen:
		shell.openWindow()
		return 0
	case wmStartWebView:
		shell.startWebView()
		return 0
	case wmWebViewResult:
		shell.completeWebView()
		return 0
	case wmTrayNotify:
		shell.trayAction(lParam)
		return 0
	case wmTimer:
		switch wParam {
		case trayRetryTimer:
			shell.registerTrayIcon()
			return 0
		case webViewTimer:
			if shell.webViewPhase == webViewLoading {
				shell.webViewResult = errors.New("WebView2 初始化超时")
				shell.completeWebView()
			}
			return 0
		}
	case wmClose:
		if shell.trayAdded || shell.addTrayIcon() {
			showWindow.Call(handle, showWindowHide)
		} else {
			message := windows.StringToUTF16Ptr("系统托盘暂时不可用。是否退出并断开隧道？")
			title := windows.StringToUTF16Ptr(windowTitle)
			choice, _, _ := messageBox.Call(handle, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x00000124) // MB_YESNO | MB_ICONQUESTION | MB_DEFBUTTON2
			if choice == 6 {                                                                                                      // IDYES
				shell.quit()
			}
		}
		return 0
	case wmEndSession:
		if wParam != 0 {
			shell.quit()
		}
		return 0
	case wmDestroy:
		shell.removeTrayIcon()
		shell.handle = 0
		postQuitMessage.Call(0)
		return 0
	}
	result, _, _ := defaultWindowProc.Call(handle, uintptr(message), wParam, lParam)
	return result
}

func (shell *windowsShell) openWindow() {
	command := uintptr(showWindowNormal)
	if minimized, _, _ := isIconic.Call(shell.handle); minimized != 0 {
		command = showWindowRestore
	}
	showWindow.Call(shell.handle, command)
	// Windows can apply the login launcher's show state on the first call.
	if !windows.IsWindowVisible(windows.HWND(shell.handle)) {
		showWindow.Call(shell.handle, command)
	}
	setForegroundWindow.Call(shell.handle)
	switch shell.webViewPhase {
	case webViewIdle:
		shell.webViewPhase = webViewLoading
		if posted, _, err := postMessage.Call(shell.handle, wmStartWebView, 0, 0); posted == 0 {
			shell.webViewResult = err
			shell.completeWebView()
		}
	case webViewFailed:
		if !shell.browserOpened {
			shell.launchBrowser()
		}
	}
}

func (shell *windowsShell) createStartupControls(handle, instance uintptr) {
	const childVisible = 0x50000000       // WS_CHILD | WS_VISIBLE
	font, _, _ := getStockObject.Call(17) // DEFAULT_GUI_FONT
	addControl := func(class, title string, style, id, top, width, height uintptr) uintptr {
		className := windows.StringToUTF16Ptr(class)
		caption := windows.StringToUTF16Ptr(title)
		control, _, _ := createWindow.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(caption)),
			childVisible|style, 24, top, width, height, handle, id, instance, 0)
		if control != 0 {
			sendMessage.Call(control, 0x0030, font, 1) // WM_SETFONT
		}
		return control
	}
	shell.notice = addControl("STATIC", "正在加载 Reverse SSH 界面…", 0, 0, 24, 550, 48)
	shell.openInBrowser = addControl("BUTTON", "在默认浏览器中打开", 0x00010000, openBrowserButton, 78, 180, 36)
}

func (shell *windowsShell) setNotice(message string) {
	if shell.notice != 0 {
		text := windows.StringToUTF16Ptr(message)
		setWindowText.Call(shell.notice, uintptr(unsafe.Pointer(text)))
	}
}

func (shell *windowsShell) launchBrowser() {
	if err := OpenURL(shell.origin.String()); err != nil {
		log.Printf("打开 Windows 默认浏览器失败：%v", err)
		shell.setNotice("无法打开默认浏览器。请设置默认浏览器后重试。")
		return
	}
	shell.browserOpened = true
}

func (shell *windowsShell) quit() {
	shell.removeTrayIcon()
	shell.webViewPhase = webViewFailed
	shell.closeWebView()
	destroyWindow.Call(shell.handle)
}

// Alert displays a native error dialog when the application cannot open.
func Alert(message string) {
	text, _ := windows.UTF16PtrFromString(message)
	title := windows.StringToUTF16Ptr(windowTitle)
	messageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x00040010)
}

// ReopenExisting requests that the existing desktop instance show its window.
func ReopenExisting() bool {
	className := windows.StringToUTF16Ptr(windowClassName)
	handle, _, _ := findWindow.Call(uintptr(unsafe.Pointer(className)), 0)
	if handle == 0 {
		return false
	}
	var pid uint32
	getWindowProcess.Call(handle, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		allowSetForeground.Call(uintptr(pid))
	}
	posted, _, _ := postMessage.Call(handle, wmReopen, 0, 0)
	return posted != 0
}

// PickKey presents a native file dialog. An empty result means it was cancelled.
func PickKey(startDir string) (string, error) {
	return pickFile(fileRequest{dir: startDir, kind: keyDialog})
}

func PickImportConfig(startDir string) (string, error) {
	return pickFile(fileRequest{dir: startDir, kind: importDialog})
}

func PickExportConfig(startDir, suggestedName string) (string, error) {
	return pickFile(fileRequest{dir: startDir, suggestedName: suggestedName, kind: exportDialog})
}

func PickExportLogs(startDir, suggestedName string) (string, error) {
	return pickFile(fileRequest{dir: startDir, suggestedName: suggestedName, kind: logsDialog})
}

func pickFile(request fileRequest) (string, error) {
	shell := activeWindow.Load()
	if shell == nil {
		return "", errors.New("应用窗口尚未就绪")
	}
	if !shell.picking.CompareAndSwap(false, true) {
		return "", errors.New("文件选择窗口已打开")
	}
	defer shell.picking.Store(false)
	request.result = make(chan fileResult, 1)
	shell.requests <- request
	if posted, _, err := postMessage.Call(shell.fileOwner, wmPickFile, 0, 0); posted == 0 {
		select {
		case <-shell.requests:
		default:
		}
		return "", err
	}
	select {
	case result := <-request.result:
		return result.path, result.err
	case <-shell.done:
		return "", nil
	}
}

type openFileName struct {
	size                       uint32
	owner, instance            uintptr
	filter, customFilter       *uint16
	maxCustomFilter, index     uint32
	file                       *uint16
	maxFile                    uint32
	fileTitle                  *uint16
	maxFileTitle               uint32
	initialDirectory, title    *uint16
	flags                      uint32
	fileOffset, extensionIndex uint16
	defaultExtension           *uint16
	customData, hook           uintptr
	templateName               *uint16
	reserved                   uintptr
	reservedFlags, extended    uint32
}

func openFileDialog(owner uintptr, request fileRequest) (string, error) {
	var directory *uint16
	if request.dir != "" {
		var err error
		directory, err = windows.UTF16PtrFromString(request.dir)
		if err != nil {
			return "", err
		}
	}
	file := make([]uint16, 32768)
	options := openFileName{
		size: uint32(unsafe.Sizeof(openFileName{})), owner: owner,
		file: &file[0], maxFile: uint32(len(file)), initialDirectory: directory,
		title: windows.StringToUTF16Ptr("选择 SSH 私钥文件"),
		flags: fileDialogFlags | fileMustExist,
	}
	if request.kind != keyDialog {
		pattern := "JSON 配置文件 (*.json)\x00*.json\x00\x00"
		if request.kind == logsDialog {
			pattern = "ZIP 压缩包 (*.zip)\x00*.zip\x00\x00"
		}
		filter := utf16.Encode([]rune(pattern))
		options.filter = &filter[0]
		options.index = 1
	}
	dialog := getOpenFileName
	switch request.kind {
	case importDialog:
		options.title = windows.StringToUTF16Ptr("导入连接配置")
	case exportDialog, logsDialog:
		title, extension := "导出连接配置", "json"
		if request.kind == logsDialog {
			title, extension = "导出诊断日志", "zip"
		}
		options.title = windows.StringToUTF16Ptr(title)
		options.flags = fileDialogFlags | confirmOverwrite
		options.defaultExtension = windows.StringToUTF16Ptr(extension)
		name, err := windows.UTF16FromString(request.suggestedName)
		if err != nil {
			return "", err
		}
		if len(name) > len(file) {
			return "", errors.New("建议的文件名过长")
		}
		copy(file, name)
		dialog = getSaveFileName
	}
	if selected, _, _ := dialog.Call(uintptr(unsafe.Pointer(&options))); selected != 0 {
		return windows.UTF16ToString(file), nil
	}
	if code, _, _ := commonDialogError.Call(); code != 0 {
		return "", fmt.Errorf("Windows 文件选择出错 (0x%04X)", code)
	}
	return "", nil
}
