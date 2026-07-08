//go:build darwin

package main

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa
void tshootStartTray(void);
*/
import "C"

import (
	"os"
	"sync/atomic"
)

var trayApp atomic.Pointer[App]

func startTray(appState *App) {
	trayApp.Store(appState)
	C.tshootStartTray()
}

//export tshootTrayOpen
func tshootTrayOpen() {
	if appState := trayApp.Load(); appState != nil {
		go appState.ShowMainWindow()
	}
}

//export tshootTrayQuit
func tshootTrayQuit() {
	if appState := trayApp.Load(); appState != nil {
		go appState.QuitApp()
		return
	}
	os.Exit(0)
}
