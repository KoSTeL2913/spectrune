package main

// Keeps automatic updates out of the user's way (requested 2026-10-22:
// the update window popping up over a game). An update restarts the
// service, which drops every tunnel for up to a minute, and used to
// bring a window to the front; neither should happen mid-game. While
// userBusy reports true, the GUI asks the service to hold the install
// (Bridge.DeferInstall), and once it goes ahead relaunchModeForUpdate
// decides how visible the update may be.

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procSHQueryUserNotificationState = modshell32.NewProc("SHQueryUserNotificationState")
	procMonitorFromWindow            = moduser32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW              = moduser32.NewProc("GetMonitorInfoW")
	procGetClassNameW                = moduser32.NewProc("GetClassNameW")
	procIsWindowVisible              = moduser32.NewProc("IsWindowVisible")
)

const (
	qunsBusy             = 2 // a full-screen app that isn't D3D
	qunsRunningD3DFull   = 3 // a full-screen Direct3D game
	qunsPresentationMode = 4
	monitorNearest       = 2
)

type monitorInfo struct {
	cbSize    uint32
	rcMonitor winRect
	rcWork    winRect
	dwFlags   uint32
}

// userBusy reports whether a game or another full-screen app has the
// screen. SHQueryUserNotificationState covers exclusive full screen and
// presentation mode; borderless "windowed full screen" games, which it
// doesn't see, are caught by foregroundIsFullscreen.
func userBusy() bool {
	var st int32
	if r, _, _ := procSHQueryUserNotificationState.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		switch st {
		case qunsBusy, qunsRunningD3DFull, qunsPresentationMode:
			return true
		}
	}
	return foregroundIsFullscreen()
}

func foregroundIsFullscreen() bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 || fg == currentHwnd {
		return false
	}
	buf := make([]uint16, 64)
	procGetClassNameW.Call(fg, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	switch windows.UTF16ToString(buf) {
	case "Progman", "WorkerW", "Shell_TrayWnd": // the desktop and taskbar
		return false
	}
	var r winRect
	procGetWindowRect.Call(fg, uintptr(unsafe.Pointer(&r)))
	mon, _, _ := procMonitorFromWindow.Call(fg, monitorNearest)
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if mon == 0 {
		return false
	}
	if ok, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return false
	}
	m := mi.rcMonitor
	return r.Left <= m.Left && r.Top <= m.Top && r.Right >= m.Right && r.Bottom >= m.Bottom
}

// Relaunch modes for the GUI after an update, passed to /gui-restart.
const (
	relaunchNormal = ""      // the user was looking at Spectrune: show it as before
	relaunchQuiet  = "quiet" // open but in the background: reopen behind other windows
	relaunchTray   = "tray"  // hidden in the tray: reopen hidden in the tray
)

// relaunchModeForUpdate picks how the update may show itself: the update
// window and a focused relaunch only when Spectrune is what the user is
// looking at.
func relaunchModeForUpdate() string {
	if vis, _, _ := procIsWindowVisible.Call(currentHwnd); vis == 0 {
		return relaunchTray
	}
	if fg, _, _ := procGetForegroundWindow.Call(); fg != currentHwnd {
		return relaunchQuiet
	}
	return relaunchNormal
}

// guiStartMode is /gui-restart's relaunch mode, read by runGUI.
var guiStartMode = relaunchNormal
