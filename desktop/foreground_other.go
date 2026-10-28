//go:build !windows

package main

// bringToFront is a no-op outside Windows; the Wails window manager raises
// shown windows on the other platforms.
func bringToFront() {}
