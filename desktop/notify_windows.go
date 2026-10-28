//go:build windows

package main

import (
	"sync"

	toast "git.sr.ht/~jackmordaunt/go-toast/v2"
)

var notifyOnce sync.Once

// notify shows a Windows toast. The installer sets the same AppUserModelID on
// the Start-menu shortcut, which is what Windows needs for unpackaged apps.
func notify(title, body string) {
	notifyOnce.Do(func() {
		_ = toast.SetAppData(toast.AppData{AppID: "Git Airlock"})
	})
	n := toast.Notification{AppID: "Git Airlock", Title: title, Body: body}
	_ = n.Push()
}
