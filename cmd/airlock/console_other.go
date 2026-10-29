//go:build !windows

package main

// hideConsole is a no-op outside Windows; the scheduled task flag is only
// used there.
func hideConsole() {}
