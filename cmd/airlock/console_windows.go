//go:build windows

package main

import (
	"os"
	"syscall"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
)

// Hide the console as early as possible when Task Scheduler starts the worker:
// main() plus flag parsing is late enough for the window to be visible.
func init() {
	if len(os.Args) < 2 || os.Args[1] != "worker" {
		return
	}
	for _, arg := range os.Args[2:] {
		if arg == "--hidden" {
			hideConsole()
			return
		}
	}
}

// hideConsole removes the console window that Windows creates for the
// console-subsystem worker when Task Scheduler starts it. The handles stay
// valid, so log writes still work.
func hideConsole() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd != 0 {
		procShowWindow.Call(hwnd, 0) // SW_HIDE
	}
}
