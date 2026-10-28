//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	procShowWindow               = user32.NewProc("ShowWindow")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadID       = kernel32.NewProc("GetCurrentThreadId")
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

// bringToFront raises this process's largest visible window and gives it
// keyboard focus. Wails' WindowShow alone only makes the window visible when
// the app is started from a shortcut while it sits in the tray.
func bringToFront() {
	pid := uint32(syscall.Getpid())
	var best uintptr
	var bestArea int64

	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var owner uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner != pid {
			return 1
		}
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
			return 1
		}
		var r winRect
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		if area := int64(r.Right-r.Left) * int64(r.Bottom-r.Top); area > bestArea {
			best, bestArea = hwnd, area
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	if best == 0 {
		return
	}

	procShowWindow.Call(best, 9) // SW_RESTORE
	// A process may only steal focus while it shares the foreground thread's
	// input queue, so attach briefly.
	fg, _, _ := procGetForegroundWindow.Call()
	var fgPid uint32
	fgThread, _, _ := procGetWindowThreadProcessID.Call(fg, uintptr(unsafe.Pointer(&fgPid)))
	myThread, _, _ := procGetCurrentThreadID.Call()
	if fgThread != 0 && fgThread != myThread {
		procAttachThreadInput.Call(myThread, fgThread, 1)
		defer procAttachThreadInput.Call(myThread, fgThread, 0)
	}
	procSetForegroundWindow.Call(best)
	procBringWindowToTop.Call(best)
}
