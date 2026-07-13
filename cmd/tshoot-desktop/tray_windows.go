//go:build windows

package main

import (
	_ "embed"

	"github.com/getlantern/systray"
)

//go:embed build/appicon.png
var trayIcon []byte

func startTray(appState *App) {
	systray.Register(func() {
		systray.SetIcon(trayIcon)
		systray.SetTooltip("Troubleshooter Studio")

		openItem := systray.AddMenuItem("打开工作台", "显示 Troubleshooter Studio 主窗口")
		systray.AddSeparator()
		quitItem := systray.AddMenuItem("退出", "退出 Troubleshooter Studio")

		go func() {
			for {
				select {
				case <-openItem.ClickedCh:
					appState.ShowMainWindow()
				case <-quitItem.ClickedCh:
					appState.QuitApp()
					systray.Quit()
					return
				}
			}
		}()
	}, nil)
}
