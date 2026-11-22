package desktop

/*
#cgo LDFLAGS: -framework Cocoa -framework WebKit -framework UniformTypeIdentifiers
#include <stdbool.h>
#include <stdlib.h>

void run_window(const char *url, bool start_hidden);
void alert_message(const char *message);
void reopen_existing(void);
enum { PickKeyFile, PickImportFile, PickExportFile };
char *choose_file(const char *start_dir, const char *suggested_name, int kind);
*/
import "C"

import "unsafe"

func Run(url string, dataDir string, startHidden bool) error {
	value := C.CString(url)
	defer C.free(unsafe.Pointer(value))
	C.run_window(value, C.bool(startHidden))
	return nil
}

func Alert(message string) {
	value := C.CString(message)
	defer C.free(unsafe.Pointer(value))
	C.alert_message(value)
}

func ReopenExisting() bool {
	C.reopen_existing()
	return true
}

func PickKey(startDir string) (string, error) {
	return chooseFile(startDir, "", C.PickKeyFile)
}

func PickImportConfig(startDir string) (string, error) {
	return chooseFile(startDir, "", C.PickImportFile)
}

func PickExportConfig(startDir, suggestedName string) (string, error) {
	return chooseFile(startDir, suggestedName, C.PickExportFile)
}

func chooseFile(startDir, suggestedName string, kind C.int) (string, error) {
	dir := C.CString(startDir)
	defer C.free(unsafe.Pointer(dir))
	name := C.CString(suggestedName)
	defer C.free(unsafe.Pointer(name))
	selected := C.choose_file(dir, name, kind)
	if selected == nil {
		return "", nil
	}
	defer C.free(unsafe.Pointer(selected))
	return C.GoString(selected), nil
}
