package fileutil

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	revealShell           = windows.NewLazySystemDLL("shell32.dll")
	parseDisplayName      = revealShell.NewProc("SHParseDisplayName")
	openFolderAndSelect   = revealShell.NewProc("SHOpenFolderAndSelectItems")
	revealOLE             = windows.NewLazySystemDLL("ole32.dll")
	initializeRevealCOM   = revealOLE.NewProc("CoInitializeEx")
	uninitializeRevealCOM = revealOLE.NewProc("CoUninitialize")
	freeRevealPIDL        = revealOLE.NewProc("CoTaskMemFree")
)

func revealFile(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("无效文件路径: %w", err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := initializeRevealCOM.Call(0, 2|4)
	// An existing apartment is already initialized; do not uninitialize its ownership.
	if uint32(hr) != 0x80010106 {
		if err := revealHRESULT("初始化 Windows Shell", hr); err != nil {
			return err
		}
		defer uninitializeRevealCOM.Call()
	}
	var pidl uintptr
	hr, _, _ = parseDisplayName.Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&pidl)), 0, 0)
	runtime.KeepAlive(name)
	if pidl != 0 {
		defer freeRevealPIDL.Call(pidl)
	}
	if err := revealHRESULT("解析 Windows Shell 文件", hr); err != nil {
		return err
	}
	if pidl == 0 {
		return fmt.Errorf("Windows Shell 未返回文件标识")
	}
	// cidl=0 selects the absolute item itself in its parent folder.
	hr, _, _ = openFolderAndSelect.Call(pidl, 0, 0, 0)
	return revealHRESULT("在资源管理器选中文件", hr)
}

func revealHRESULT(operation string, result uintptr) error {
	if int32(result) < 0 {
		return fmt.Errorf("%s失败 (HRESULT 0x%08X)", operation, uint32(result))
	}
	return nil
}
