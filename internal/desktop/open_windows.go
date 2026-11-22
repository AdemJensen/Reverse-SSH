package desktop

import (
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

func openBrowser(address string) error {
	location, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	status, _, _ := coInitialize.Call(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if int32(status) < 0 {
		return syscall.Errno(uint32(status))
	}
	defer coUninitialize.Call()
	return windows.ShellExecute(0, nil, location, nil, nil, windows.SW_SHOWNORMAL)
}
