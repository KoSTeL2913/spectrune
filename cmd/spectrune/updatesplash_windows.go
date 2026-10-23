package main

// Update window: a small borderless box (no title bar, no close button)
// with a running progress bar that takes over from the main GUI for the
// whole update. "Check for updates" closes the main window and opens it in
// check mode — it asks the service for a newer release, and either
// installs it or says the app is up to date, then reopens the GUI. An
// automatic update noticed while the GUI is open (checkBackgroundUpdate)
// opens it straight in install mode. Before this (up to v2.0.56.0) the GUI
// showed an overlay for 1.5s and closed, and nothing at all was on screen
// for the 30s-2.5min the uninstall + install took.
//
// It can't run from the installed spectrune.exe — any process using that
// file blocks msiexec from replacing it (the whole reason the GUI closes
// at all). So the GUI copies the exe to
// %LocalAppData%\Spectrune\spectrune-update.exe and this runs from that
// copy (/update-splash), started through an unelevated interactive
// Scheduled Task like the /gui-restart relaunch always has been.

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const (
	splashModeCheck   = "check"   // "Check for updates" button
	splashModeInstall = "install" // update already downloading/installing

	// splashLoadingTitle is the window's title until its page has painted;
	// it only switches to updateSplashInfo.Title then, which is what the
	// closing GUI waits for (waitForWindow) — so the GUI never closes onto
	// a blank, still-loading window.
	splashLoadingTitle = "Spectrune update (loading)"

	// guiReadyEventName is signaled by a GUI whose page has painted
	// (runGUI's uiReady binding); the update window stays on top until the
	// relaunched GUI sets it, covering its blank first second.
	guiReadyEventName = `Local\SpectruneGUIReady`

	splashWidth  = 360
	splashHeight = 128
	splashRadius = 8 // matches Windows 11's own DWM corner radius
)

// updateSplashInfo is handed from the closing GUI to the update window
// through a JSON file next to the copied exe (one Scheduled Task /tr
// string can't carry it without a quoting minefield).
type updateSplashInfo struct {
	Mode    string
	ExePath string // installed spectrune.exe to relaunch
	// Relaunch is the GUI's mode after the update (relaunchModeForUpdate).
	// Anything but relaunchNormal means the user is busy elsewhere, so no
	// update window is shown at all — this process only waits for the
	// install and reopens the GUI.
	Relaunch   string
	OldVersion string // the version being replaced, for the "done" check
	GUIRect    winRect

	// From the GUI's setUpdateSplashStyle, so the window matches its
	// language and theme. %s in TextInstalling/TextUpToDate is the
	// version, in TextFailed the error.
	Title          string
	TextChecking   string
	TextInstalling string
	TextUpToDate   string
	TextFailed     string
	Light          bool
	Grad           string // CSS gradient for the progress bar
}

var pendingSplashStyle = updateSplashInfo{
	Title:          "Spectrune — updating",
	TextChecking:   "Checking for updates…",
	TextInstalling: "Installing update %s…",
	TextUpToDate:   "You're on the latest version (%s)",
	TextFailed:     "Couldn't check for updates: %s",
	Grad:           "linear-gradient(135deg, #7a5cff 0%, #ff3ea5 100%)",
}

var (
	procSetWindowPos       = moduser32.NewProc("SetWindowPos")
	procGetWindowRect      = moduser32.NewProc("GetWindowRect")
	procSetWindowTextW     = moduser32.NewProc("SetWindowTextW")
	procSetWindowsHookExW  = moduser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHook  = moduser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx     = moduser32.NewProc("CallNextHookEx")
	procGetCurrentThreadID = modkernel32.NewProc("GetCurrentThreadId")
	procSetWindowRgn       = moduser32.NewProc("SetWindowRgn")
	procCreateRoundRectRgn = modgdi32.NewProc("CreateRoundRectRgn")
)

const (
	wsPopup          = 0x80000000
	wsVisible        = 0x10000000
	whCBT            = 5
	hcbtCreateWnd    = 3
	hwndTopmost      = ^uintptr(0) // (HWND)-1
	swpNoZOrder      = 0x0004
	swpFrameChg      = 0x0020
	swpShowWin       = 0x0040
	swpHideWin       = 0x0080
	swpNoActivate    = 0x0010
	hwndBottom       = 1
	smCxScreen       = 0
	smCyScreen       = 1
	smXVirtualScreen = 76
	smYVirtualScreen = 77
	swpNoSize        = 0x0001
	dwmwaCorner      = 33 // DWMWA_WINDOW_CORNER_PREFERENCE (Windows 11)
	dwmwcpRound      = 2
)

func updateSplashDir() (string, error) {
	dir, err := os.UserCacheDir() // %LocalAppData%
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Spectrune"), nil
}

// launchUpdateSplash copies the running exe, writes the info file, starts
// the copy through a Scheduled Task and returns once its window has
// painted (or after a timeout — the caller closes the GUI either way).
func launchUpdateSplash(mode string, guiHwnd uintptr, relaunch string) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := updateSplashDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	splashExe := filepath.Join(dir, "spectrune-update.exe")
	if err := copyFile(exePath, splashExe); err != nil {
		return err
	}
	info := pendingSplashStyle
	info.Mode = mode
	info.ExePath = exePath
	info.OldVersion = appVersion
	info.Relaunch = relaunch
	procGetWindowRect.Call(guiHwnd, uintptr(unsafe.Pointer(&info.GUIRect)))
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "update-splash.json"), data, 0o600); err != nil {
		return err
	}
	createArgs := []string{
		"/create", "/tn", "SpectruneRelaunchAfterUpdate",
		"/tr", fmt.Sprintf(`"%s" /update-splash`, splashExe),
		"/sc", "once", "/st", time.Now().Add(2 * time.Second).Format("15:04:05"), "/it", "/f",
	}
	if out, err := hiddenCommand("schtasks", createArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /create: %w (%s)", err, out)
	}
	// Explicit /run as well — see closeForUpdate for why the /st trigger
	// alone isn't trusted.
	if out, err := hiddenCommand("schtasks", "/run", "/tn", "SpectruneRelaunchAfterUpdate").CombinedOutput(); err != nil {
		log.Printf("launchUpdateSplash: schtasks /run: %v (%s)", err, out)
	}
	// This GUI is about to go away; its ready event must not make the
	// update window think the relaunched GUI is already up.
	closeGUIReadyEvent()
	if relaunch == relaunchNormal {
		waitForWindow(info.Title, 15*time.Second)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func waitForWindow(title string, timeout time.Duration) bool {
	titlePtr, _ := windows.UTF16PtrFromString(title)
	deadline := time.Now().Add(timeout)
	for {
		if hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr))); hwnd != 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// hiddenCommand runs a console tool from a GUI-subsystem process without
// Windows allocating (and flashing) a console window for it.
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

var guiReadyEvent windows.Handle

// signalGUIReady is called once the GUI's page has painted.
func signalGUIReady() {
	if guiReadyEvent != 0 {
		return
	}
	name, _ := windows.UTF16PtrFromString(guiReadyEventName)
	if h, err := windows.CreateEvent(nil, 1, 1, name); err == nil {
		guiReadyEvent = h
	}
}

func closeGUIReadyEvent() {
	if guiReadyEvent != 0 {
		windows.CloseHandle(guiReadyEvent)
		guiReadyEvent = 0
	}
}

func waitForGUIReady(timeout time.Duration) {
	name, _ := windows.UTF16PtrFromString(guiReadyEventName)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h, err := windows.OpenEvent(windows.SYNCHRONIZE, false, name); err == nil {
			windows.CloseHandle(h)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// cleanupUpdateSplash removes the previous update's exe copy. Called from
// runGUI in the background: right after an update the update window may
// still be alive for a moment (it waits for this very GUI), so retry.
func cleanupUpdateSplash() {
	dir, err := updateSplashDir()
	if err != nil {
		return
	}
	splashExe := filepath.Join(dir, "spectrune-update.exe")
	for i := 0; i < 20; i++ {
		if err := os.Remove(splashExe); err == nil || os.IsNotExist(err) {
			os.Remove(filepath.Join(dir, "update-splash.json"))
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// installFinished reports whether the self-update transaction is over:
// scheduleInstall's SpectruneSelfUpdateInstall task seen and then gone
// (its last line deletes itself, after both msiexec runs), or — should
// the poll miss the task entirely — gone with the service already
// reporting a different version.
func installFinished(seen *bool, oldVersion string) bool {
	if hiddenCommand("schtasks.exe", "/query", "/tn", "SpectruneSelfUpdateInstall").Run() == nil {
		*seen = true
		return false
	}
	if *seen {
		return true
	}
	client, err := ipcDial()
	if err != nil {
		return false
	}
	defer client.Close()
	var v string
	return client.Call("Bridge.Version", struct{}{}, &v) == nil && v != "" && v != oldVersion
}

// checkAndInstall is check mode's job; returns the status line to show.
// installing reports whether an install was started.
func checkAndInstall(info updateSplashInfo, setStatus func(string)) (installing bool) {
	var reply UpdateCheckReply
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var client *rpc.Client
		client, err = ipcDial()
		if err == nil {
			err = client.Call("Bridge.CheckForUpdateNow", struct{}{}, &reply)
			client.Close()
		}
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	switch {
	case err != nil:
		setStatus(fmt.Sprintf(info.TextFailed, err))
		time.Sleep(4 * time.Second)
		return false
	case !reply.Available:
		setStatus(fmt.Sprintf(info.TextUpToDate, reply.Current))
		time.Sleep(2500 * time.Millisecond)
		return false
	}
	setStatus(fmt.Sprintf(info.TextInstalling, reply.Latest))
	// Queued service-side until the MSI has finished downloading.
	if client, err := ipcDial(); err == nil {
		client.Call("Bridge.TriggerInstall", struct{}{}, new(struct{}))
		client.Close()
	}
	return true
}

func runUpdateSplash() {
	dir, err := updateSplashDir()
	if err != nil {
		log.Fatalf("update splash: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "update-splash.json"))
	if err != nil {
		log.Fatalf("update splash: %v", err)
	}
	var info updateSplashInfo
	if err := json.Unmarshal(data, &info); err != nil {
		log.Fatalf("update splash: %v", err)
	}

	// Dark until the page paints, in case the window is revealed first.
	bg := "FF1A1826"
	if info.Light {
		bg = "FFFFFFFF"
	}
	os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", bg)
	var w webview2.WebView
	var hwnd uintptr
	if info.Relaunch == relaunchNormal {
		w, hwnd = newOffscreenWebview(webview2.WebViewOptions{
			DataPath: filepath.Join(dir, "update-splash-webview"),
			WindowOptions: webview2.WindowOptions{
				Title:  splashLoadingTitle,
				Width:  splashWidth,
				Height: splashHeight,
			},
		})
	}
	if w != nil {
		makeSplashWindow(hwnd)
	}
	setStatus := func(text string) {
		if w == nil {
			return
		}
		js := "setStatus(" + strconv.Quote(text) + ")"
		w.Dispatch(func() { w.Eval(js) })
	}

	// The update itself runs independently of the window.
	done := make(chan struct{})
	go func() {
		defer close(done)
		installing := info.Mode == splashModeInstall
		if info.Mode == splashModeCheck {
			installing = checkAndInstall(info, setStatus)
		}
		if installing {
			seen := false
			// Generous: a background update on a slow VM took ~2.5 min end
			// to end (2026-10-12). Relaunch anyway at the deadline so a
			// stuck install never leaves the user with no app at all.
			deadline := time.Now().Add(6 * time.Minute)
			for time.Now().Before(deadline) {
				time.Sleep(2 * time.Second)
				if installFinished(&seen, info.OldVersion) {
					break
				}
			}
		}
		args := []string{"/gui-restart"}
		if info.Relaunch != relaunchNormal {
			args = append(args, info.Relaunch)
		}
		if err := exec.Command(info.ExePath, args...).Start(); err != nil {
			log.Printf("update splash: relaunch: %v", err)
			return
		}
		waitForGUIReady(20 * time.Second)
	}()

	if w == nil {
		<-done // silent, or no WebView2 — still update and relaunch, just without a window
		return
	}
	var revealOnce sync.Once
	reveal := func() { revealOnce.Do(func() { revealSplash(hwnd, info) }) }
	if err := w.Bind("splashReady", reveal); err != nil {
		log.Printf("update splash: bind: %v", err)
	}
	// Fallback in case the page never reports in.
	time.AfterFunc(3*time.Second, func() { w.Dispatch(reveal) })
	w.SetHtml(updateSplashHTML(info))
	go func() {
		<-done
		w.Dispatch(func() { procDestroyWindow.Call(hwnd) })
	}()
	w.Run()
	w.Destroy()
	<-done
}

// newOffscreenWebview creates a WebView2 window off screen: go-webview2
// shows its window before WebView2 has attached to it, which on a slow
// machine means a couple of seconds of a blank white box (seen on the
// win10 VM, 2026-10-12). A thread-local CBT hook moves the window's
// creation position beyond every monitor; the caller moves it into place
// once the page has painted (revealSplash, revealMainWindow).
// Not -32000: Windows treats that as "minimized" and parked the window
// visibly at the bottom-left of the screen instead.
func newOffscreenWebview(opts webview2.WebViewOptions) (webview2.WebView, uintptr) {
	runtime.LockOSThread() // the hook is per-thread; so is the window
	ox, oy := offscreenPos()
	moved := false
	hook := windows.NewCallback(func(code, wparam, lparam uintptr) uintptr {
		if int32(code) == hcbtCreateWnd && !moved {
			cw := (*cbtCreateWnd)(unsafe.Pointer(lparam))
			if cw.lpcs.hwndParent == 0 {
				cw.lpcs.x, cw.lpcs.y = ox, oy
				moved = true
			}
		}
		r, _, _ := procCallNextHookEx.Call(0, code, wparam, lparam)
		return r
	})
	tid, _, _ := procGetCurrentThreadID.Call()
	hh, _, _ := procSetWindowsHookExW.Call(whCBT, hook, 0, tid)
	w := webview2.NewWithOptions(opts)
	if hh != 0 {
		procUnhookWindowsHook.Call(hh)
	}
	if w == nil {
		return nil, 0
	}
	return w, uintptr(w.Window())
}

func offscreenPos() (int32, int32) {
	vx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	vy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	return int32(vx) - 8000, int32(vy) - 8000
}

// makeSplashWindow turns the update window borderless (no title bar, so
// no close/minimize/maximize buttons) with rounded corners.
func makeSplashWindow(hwnd uintptr) {
	procSetWindowLongPtrW.Call(hwnd, uintptr(int(gwlStyle)), wsPopup|wsVisible)
	// Rounded corners: Windows 11 does them (anti-aliased) via DWM;
	// Windows 10 rejects that attribute, so clip the window to a rounded
	// region instead.
	if err := dwmSetWindowAttribute(hwnd, dwmwaCorner, dwmwcpRound); err != nil {
		rgn, _, _ := procCreateRoundRectRgn.Call(0, 0, splashWidth+1, splashHeight+1, 2*splashRadius, 2*splashRadius)
		procSetWindowRgn.Call(hwnd, rgn, 1) // the system owns rgn from here on
	}
	setWindowIconFromResource(hwnd)
	ox, oy := offscreenPos()
	procSetWindowPos.Call(hwnd, 0, uintptr(ox), uintptr(oy), splashWidth, splashHeight, swpNoZOrder|swpFrameChg)
}

// revealMainWindow moves the main GUI, created off screen by
// newOffscreenWebview, to the center of the screen once its page has
// painted (uiReady) — or after a fallback timeout.
// mode is guiStartMode: after an update the user wasn't watching, the
// window reopens behind everything else (relaunchQuiet) or stays hidden
// in the tray (relaunchTray) instead of jumping to the front.
func revealMainWindow(hwnd uintptr, mode string) {
	var r winRect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	w, h := r.Right-r.Left, r.Bottom-r.Top
	sw, _, _ := procGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCyScreen)
	x, y := (int32(sw)-w)/2, (int32(sh)-h)/2
	switch mode {
	case relaunchTray:
		procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate|swpHideWin)
	case relaunchQuiet:
		procSetWindowPos.Call(hwnd, hwndBottom, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoActivate|swpShowWin)
	default:
		procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpShowWin)
		forceForeground(hwnd)
	}
}

type createStruct struct {
	lpCreateParams uintptr
	hInstance      uintptr
	hMenu          uintptr
	hwndParent     uintptr
	cy, cx, y, x   int32
	style          int32
	lpszName       uintptr
	lpszClass      uintptr
	dwExStyle      uint32
}

type cbtCreateWnd struct {
	lpcs            *createStruct
	hwndInsertAfter uintptr
}

// revealSplash moves the painted window into place — centered on where
// the main GUI was, or on the screen — always on top, and switches it to
// its real title so the closing GUI knows it can go.
func revealSplash(hwnd uintptr, info updateSplashInfo) {
	r := info.GUIRect
	var cx, cy int32
	if r.Right > r.Left && r.Left > -10000 {
		cx, cy = (r.Left+r.Right)/2, (r.Top+r.Bottom)/2
	} else {
		sw, _, _ := procGetSystemMetrics.Call(smCxScreen)
		sh, _, _ := procGetSystemMetrics.Call(smCyScreen)
		cx, cy = int32(sw)/2, int32(sh)/2
	}
	x, y := cx-splashWidth/2, cy-splashHeight/2
	procSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), splashWidth, splashHeight, swpShowWin)
	title, _ := windows.UTF16PtrFromString(info.Title)
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(title)))
	forceForeground(hwnd)
}

func updateSplashHTML(info updateSplashInfo) string {
	scheme, ink, muted, base, border, track := "dark", "#eef0f8", "#9693ad", "#1a1826", "#322f47", "#322f47"
	if info.Light {
		scheme, ink, muted, base, border, track = "light", "#1c1b29", "#6b6980", "#ffffff", "#e3e1ed", "#e3e1ed"
	}
	first := info.TextInstalling
	if info.Mode == splashModeCheck {
		first = info.TextChecking
	} else {
		// Install mode doesn't know the version — drop the placeholder.
		first = strings.Replace(first, " %s", "", 1)
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><style>
  :root { color-scheme: %s; }
  html, body { height: 100%%; margin: 0; overflow: hidden; }
  body {
    background: %s; color: %s; font-family: "Segoe UI", sans-serif; font-size: 13.5px;
    box-sizing: border-box; border: 1px solid %s; border-radius: %dpx; padding: 18px 22px;
    display: flex; flex-direction: column; justify-content: center;
    user-select: none; cursor: default;
  }
  .brand { display: flex; align-items: center; gap: 9px; font-weight: 600; font-size: 15px; }
  .logo {
    width: 22px; height: 22px; border-radius: 6px; background: %s;
    color: #fff; font-size: 13px; display: flex; align-items: center; justify-content: center;
  }
  #status { margin-top: 12px; color: %s; line-height: 1.4; min-height: 19px; }
  .track { margin-top: 12px; height: 4px; border-radius: 2px; overflow: hidden; background: %s; }
  .fill {
    height: 100%%; width: 40%%; border-radius: 2px; background: %s;
    animation: run 1.3s ease-in-out infinite;
  }
  .done .fill { width: 100%%; animation: none; }
  @keyframes run { 0%% { transform: translateX(-100%%); } 100%% { transform: translateX(250%%); } }
</style></head>
<body>
  <div class="brand"><div class="logo">S</div>Spectrune</div>
  <div id="status">%s</div>
  <div class="track"><div class="fill"></div></div>
<script>
  function setStatus(text) { document.getElementById('status').textContent = text; }
  // Not requestAnimationFrame: the window is still off screen, where
  // Chromium may treat it as occluded and never run animation frames.
  window.addEventListener('load', function() { setTimeout(splashReady, 50); });
</script>
</body></html>`,
		scheme, base, ink, border, splashRadius, info.Grad, muted, track, info.Grad, html.EscapeString(first))
}
