package desktop

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/combridge"
	"github.com/wailsapp/go-webview2/pkg/edge"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

type webViewPhase uint8

const (
	webViewIdle webViewPhase = iota
	webViewLoading
	webViewReady
	webViewFailed

	webViewTimer   = 2
	webViewTimeout = 10000 // milliseconds
)

// Native vtable slots are indexed from the first IUnknown method.
const (
	createControllerSlot        = 3
	closeControllerSlot         = 24
	putBoundsSlot               = 6
	addNavigationStartingSlot   = 7
	addNavigationCompletedSlot  = 15
	addFrameNavigationStartSlot = 17
	addPermissionRequestedSlot  = 23
	addProcessFailedSlot        = 25
	addNewWindowRequestedSlot   = 44
	getNavigationURISlot        = 3
	getNavigationSuccessSlot    = 3
	getNavigationErrorSlot      = 4
	getFailedProcessKindSlot    = 3
	putNewWindowHandledSlot     = 6
	putPermissionStateSlot      = 7
	putNavigationCancelSlot     = 8
)

type nativeWebViewObject struct {
	methods *[45]edge.ComProc
}

func (shell *windowsShell) startWebView() {
	if shell.handle == 0 || shell.webViewPhase != webViewLoading {
		return
	}
	if shell.comError != nil {
		shell.postWebViewResult(shell.comError)
		return
	}
	if timer, _, err := setTimer.Call(shell.handle, webViewTimer, webViewTimeout, 0); timer == 0 {
		shell.postWebViewResult(fmt.Errorf("创建 WebView2 启动计时器：%w", err))
		return
	}
	if err := os.MkdirAll(shell.webViewDataDir, 0700); err != nil {
		shell.postWebViewResult(fmt.Errorf("创建 WebView2 数据目录：%w", err))
		return
	}
	log.Print("正在初始化 Windows WebView2")
	if err := webviewloader.CreateCoreWebView2EnvironmentWithOptions(
		shell, webviewloader.WithUserDataFolder(shell.webViewDataDir),
	); err != nil {
		shell.postWebViewResult(fmt.Errorf("启动 WebView2 环境：%w", err))
	}
}

func (shell *windowsShell) EnvironmentCompleted(code webviewloader.HRESULT, environment *webviewloader.ICoreWebView2Environment) webviewloader.HRESULT {
	if shell.handle == 0 || shell.webViewPhase != webViewLoading {
		return 0
	}
	if code < 0 || environment == nil {
		shell.postWebViewResult(fmt.Errorf("创建 WebView2 环境：HRESULT 0x%08x", uint32(code)))
		return 0
	}
	handler := combridge.New[controllerCompletedCallback](shell)
	defer handler.Close()
	native := (*nativeWebViewObject)(unsafe.Pointer(environment))
	result, _, _ := native.methods[createControllerSlot].Call(
		uintptr(unsafe.Pointer(environment)), shell.handle, handler.Ref(),
	)
	if int32(result) < 0 {
		shell.postWebViewResult(fmt.Errorf("启动 WebView2 控制器：HRESULT 0x%08x", uint32(result)))
	}
	return 0
}

func (shell *windowsShell) controllerCompleted(code uint32, controller *edge.ICoreWebView2Controller) uintptr {
	if shell.handle == 0 || shell.webViewPhase != webViewLoading {
		if controller != nil {
			closeWebViewController(controller)
		}
		return 0
	}
	if int32(code) < 0 || controller == nil {
		shell.postWebViewResult(fmt.Errorf("创建 WebView2 控制器：HRESULT 0x%08x", code))
		return 0
	}
	controller.AddRef()
	shell.webView = controller
	if err := shell.navigateWebView(); err != nil {
		shell.postWebViewResult(err)
	}
	return 0
}

func (shell *windowsShell) navigateWebView() error {
	controller := shell.webView
	if err := controller.PutIsVisible(false); err != nil {
		return fmt.Errorf("初始化 WebView2 可见性：%w", err)
	}
	if err := shell.resizeWebView(); err != nil {
		return fmt.Errorf("初始化 WebView2 窗口尺寸：%w", err)
	}
	core, err := controller.GetCoreWebView2()
	if err != nil {
		return fmt.Errorf("获取 WebView2 界面：%w", err)
	}
	defer core.Release()
	if err := configureWebView(controller, core); err != nil {
		return fmt.Errorf("配置 WebView2：%w", err)
	}
	if err := shell.registerWebViewEvents(core); err != nil {
		return fmt.Errorf("注册 WebView2 事件：%w", err)
	}
	if err := core.Navigate(shell.origin.String()); err != nil {
		return fmt.Errorf("载入本机界面：%w", err)
	}
	return nil
}

func configureWebView(controller *edge.ICoreWebView2Controller, core *edge.ICoreWebView2) error {
	settings, err := core.GetSettings()
	if err != nil {
		return err
	}
	defer settings.Release()
	disableSettings := []func(bool) error{
		settings.PutAreDevToolsEnabled, settings.PutAreDefaultContextMenusEnabled,
		settings.PutAreHostObjectsAllowed, settings.PutIsWebMessageEnabled, settings.PutIsStatusBarEnabled,
	}
	if extended := settings.GetICoreWebView2Settings4(); extended != nil {
		defer combridge.IUnknownFromPointer(unsafe.Pointer(extended)).Release()
		disableSettings = append(disableSettings, extended.PutIsPasswordAutosaveEnabled, extended.PutIsGeneralAutofillEnabled)
	}
	if extended := controller.GetICoreWebView2Controller4(); extended != nil {
		defer combridge.IUnknownFromPointer(unsafe.Pointer(extended)).Release()
		disableSettings = append(disableSettings, extended.PutAllowExternalDrop)
	}
	for _, disable := range disableSettings {
		if err := disable(false); err != nil {
			return err
		}
	}
	return nil
}

func (shell *windowsShell) postWebViewResult(err error) {
	if shell.handle == 0 || (shell.webViewResult != nil && err == nil) {
		return
	}
	shell.webViewResult = err
	postMessage.Call(shell.handle, wmWebViewResult, 0, 0)
}

func (shell *windowsShell) completeWebView() {
	err := shell.webViewResult
	shell.webViewResult = nil
	if shell.webViewPhase != webViewLoading && (shell.webViewPhase != webViewReady || err == nil) {
		return
	}
	killTimer.Call(shell.handle, webViewTimer)
	if err == nil {
		if shell.webView == nil {
			err = errors.New("WebView2 未返回控制器")
		} else {
			err = shell.webView.PutIsVisible(true)
		}
	}
	if err == nil {
		shell.webViewPhase = webViewReady
		showWindow.Call(shell.notice, showWindowHide)
		showWindow.Call(shell.openInBrowser, showWindowHide)
		log.Print("Windows WebView2 已载入界面")
		return
	}

	shell.webViewPhase = webViewFailed
	shell.closeWebView()
	log.Printf("Windows WebView2 无法使用，保留本机窗口和托盘：%v", err)
	shell.setNotice("嵌入式界面无法加载。可在默认浏览器中管理 SSH 隧道。")
	showWindow.Call(shell.notice, showWindowNormal)
	showWindow.Call(shell.openInBrowser, showWindowNormal)
	if !shell.browserOpened && windows.IsWindowVisible(windows.HWND(shell.handle)) {
		shell.launchBrowser()
	}
}

func (shell *windowsShell) resizeWebView() error {
	var bounds edge.Rect
	if result, _, err := getClientRect.Call(shell.handle, uintptr(unsafe.Pointer(&bounds))); result == 0 {
		return err
	}
	controller := (*nativeWebViewObject)(unsafe.Pointer(shell.webView))
	if runtime.GOARCH == "arm64" {
		// Windows ARM64 passes a 16-byte by-value RECT in two registers.
		pack := func(first, second int32) uintptr {
			return uintptr(uint64(uint32(first)) | uint64(uint32(second))<<32)
		}
		result, _, _ := controller.methods[putBoundsSlot].Call(
			uintptr(unsafe.Pointer(controller)), pack(bounds.Left, bounds.Top), pack(bounds.Right, bounds.Bottom),
		)
		if int32(result) < 0 {
			return syscall.Errno(uint32(result))
		}
		return nil
	}
	return shell.webView.PutBounds(bounds)
}

func closeWebViewController(controller *edge.ICoreWebView2Controller) {
	native := (*nativeWebViewObject)(unsafe.Pointer(controller))
	native.methods[closeControllerSlot].Call(uintptr(unsafe.Pointer(controller)))
}

func (shell *windowsShell) closeWebView() {
	if shell.webView != nil {
		controller := shell.webView
		shell.webView = nil
		closeWebViewController(controller)
		controller.Release()
	}
}

type controllerCompletedCallback interface {
	controllerCompleted(uint32, *edge.ICoreWebView2Controller) uintptr
}
type navigationStartingCallback interface {
	navigationStarting(*nativeWebViewObject) uintptr
}
type navigationCompletedCallback interface {
	navigationCompleted(*nativeWebViewObject) uintptr
}
type newWindowCallback interface {
	blockNewWindow(*nativeWebViewObject) uintptr
}
type permissionCallback interface {
	denyPermission(*nativeWebViewObject) uintptr
}
type processFailedCallback interface {
	processFailed(*nativeWebViewObject) uintptr
}

func init() {
	combridge.RegisterVTable[combridge.IUnknown, controllerCompletedCallback]("{6C4819F3-C9B7-4260-8127-C9F5BDE7F68C}",
		func(this uintptr, code uint32, controller *edge.ICoreWebView2Controller) uintptr {
			return combridge.Resolve[controllerCompletedCallback](this).controllerCompleted(code, controller)
		})
	combridge.RegisterVTable[combridge.IUnknown, navigationStartingCallback]("{9ADBE429-F36D-432B-9DDC-F8881FBD76E3}",
		func(this, _ uintptr, args *nativeWebViewObject) uintptr {
			return combridge.Resolve[navigationStartingCallback](this).navigationStarting(args)
		})
	combridge.RegisterVTable[combridge.IUnknown, navigationCompletedCallback]("{D33A35BF-1C49-4F98-93AB-006E0533FE1C}",
		func(this, _ uintptr, args *nativeWebViewObject) uintptr {
			return combridge.Resolve[navigationCompletedCallback](this).navigationCompleted(args)
		})
	combridge.RegisterVTable[combridge.IUnknown, newWindowCallback]("{D4C185FE-C81C-4989-97AF-2D3FA7AB5651}",
		func(this, _ uintptr, args *nativeWebViewObject) uintptr {
			return combridge.Resolve[newWindowCallback](this).blockNewWindow(args)
		})
	combridge.RegisterVTable[combridge.IUnknown, permissionCallback]("{15E1C6A3-C72A-4DF3-91D7-D097FBEC6BFD}",
		func(this, _ uintptr, args *nativeWebViewObject) uintptr {
			return combridge.Resolve[permissionCallback](this).denyPermission(args)
		})
	combridge.RegisterVTable[combridge.IUnknown, processFailedCallback]("{79E0AEA4-990B-42D9-AA1D-0FCC2E5BC7F1}",
		func(this, _ uintptr, args *nativeWebViewObject) uintptr {
			return combridge.Resolve[processFailedCallback](this).processFailed(args)
		})
}

func (shell *windowsShell) registerWebViewEvents(core *edge.ICoreWebView2) error {
	navigating := combridge.New[navigationStartingCallback](shell)
	defer navigating.Close()
	navigated := combridge.New[navigationCompletedCallback](shell)
	defer navigated.Close()
	newWindow := combridge.New[newWindowCallback](shell)
	defer newWindow.Close()
	permission := combridge.New[permissionCallback](shell)
	defer permission.Close()
	failed := combridge.New[processFailedCallback](shell)
	defer failed.Close()
	native := (*nativeWebViewObject)(unsafe.Pointer(core))
	events := []struct {
		slot    int
		handler uintptr
	}{
		{addNavigationStartingSlot, navigating.Ref()},
		{addFrameNavigationStartSlot, navigating.Ref()},
		{addNavigationCompletedSlot, navigated.Ref()},
		{addNewWindowRequestedSlot, newWindow.Ref()},
		{addPermissionRequestedSlot, permission.Ref()},
		{addProcessFailedSlot, failed.Ref()},
	}
	for _, event := range events {
		var token int64
		result, _, _ := native.methods[event.slot].Call(
			uintptr(unsafe.Pointer(core)), event.handler, uintptr(unsafe.Pointer(&token)),
		)
		if int32(result) < 0 {
			return syscall.Errno(uint32(result))
		}
	}
	return nil
}

func (shell *windowsShell) navigationStarting(args *nativeWebViewObject) uintptr {
	var rawURI *uint16
	result, _, _ := args.methods[getNavigationURISlot].Call(
		uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&rawURI)),
	)
	if int32(result) >= 0 && rawURI != nil {
		uri, err := url.Parse(windows.UTF16PtrToString(rawURI))
		windows.CoTaskMemFree(unsafe.Pointer(rawURI))
		if err == nil && uri.Scheme == shell.origin.Scheme && uri.Host == shell.origin.Host && uri.User == nil {
			return 0
		}
	}
	result, _, _ = args.methods[putNavigationCancelSlot].Call(uintptr(unsafe.Pointer(args)), 1)
	return result
}

func (shell *windowsShell) navigationCompleted(args *nativeWebViewObject) uintptr {
	if shell.webViewPhase != webViewLoading {
		return 0
	}
	var loaded int32
	result, _, _ := args.methods[getNavigationSuccessSlot].Call(
		uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&loaded)),
	)
	if int32(result) < 0 || loaded == 0 {
		var status int32
		args.methods[getNavigationErrorSlot].Call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&status)))
		shell.postWebViewResult(fmt.Errorf("WebView2 页面加载失败，状态 %d", status))
		return 0
	}
	shell.postWebViewResult(nil)
	return 0
}

func (*windowsShell) blockNewWindow(args *nativeWebViewObject) uintptr {
	result, _, _ := args.methods[putNewWindowHandledSlot].Call(uintptr(unsafe.Pointer(args)), 1)
	return result
}

func (*windowsShell) denyPermission(args *nativeWebViewObject) uintptr {
	result, _, _ := args.methods[putPermissionStateSlot].Call(
		uintptr(unsafe.Pointer(args)), uintptr(edge.CoreWebView2PermissionStateDeny),
	)
	return result
}

func (shell *windowsShell) processFailed(args *nativeWebViewObject) uintptr {
	var kind int32
	result, _, _ := args.methods[getFailedProcessKindSlot].Call(
		uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&kind)),
	)
	if int32(result) >= 0 && (kind == 0 || kind == 1) {
		shell.postWebViewResult(errors.New("WebView2 浏览器进程已退出"))
	}
	return 0
}
