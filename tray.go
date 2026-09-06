// System tray icon that mirrors connection state — colored glyph while a
// tunnel is up, grey while disconnected, exactly like the Linux awg-gui
// counterpart (its AppIndicator flips between "awg-gui-active" and
// "awg-gui-inactive" — see awg_gui.py's set_active). Both apps render from
// the same two source SVGs so the two platforms share one visual identity;
// here they're pre-rasterized to PNG and embedded, since this is raw Win32
// with no SVG decoder available.
//
// Needs its own hidden window + dedicated message loop: window messages
// are only delivered to the thread that created the window, and this must
// stay independent of the WebView2 window's own loop (webview.Run(), which
// already claims WM_APP for its internal dispatch queue — see
// go-webview2's webview.go).
package main

import (
	_ "embed"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/amnezia-vpn/amneziawg-windows/v3/conf"
)

//go:embed assets/tray-active.png
var trayActivePNG []byte

//go:embed assets/tray-inactive.png
var trayInactivePNG []byte

var (
	procShellNotifyIconW         = modshell32.NewProc("Shell_NotifyIconW")
	procCreateIconFromResourceEx = moduser32.NewProc("CreateIconFromResourceEx")
	procRegisterClassExW         = moduser32.NewProc("RegisterClassExW")
	procCreateWindowExW          = moduser32.NewProc("CreateWindowExW")
	procDefWindowProcW           = moduser32.NewProc("DefWindowProcW")
	procDestroyWindow            = moduser32.NewProc("DestroyWindow")
	procPostQuitMessage          = moduser32.NewProc("PostQuitMessage")
	procGetMessageW              = moduser32.NewProc("GetMessageW")
	procTranslateMessage         = moduser32.NewProc("TranslateMessage")
	procDispatchMessageW         = moduser32.NewProc("DispatchMessageW")
	procCreatePopupMenu          = moduser32.NewProc("CreatePopupMenu")
	procAppendMenuW              = moduser32.NewProc("AppendMenuW")
	procTrackPopupMenu           = moduser32.NewProc("TrackPopupMenu")
	procDestroyMenu              = moduser32.NewProc("DestroyMenu")
	procSetForegroundWindow      = moduser32.NewProc("SetForegroundWindow")
	procGetCursorPos             = moduser32.NewProc("GetCursorPos")
	procGetSystemMetrics         = moduser32.NewProc("GetSystemMetrics")
	procPostMessageW             = moduser32.NewProc("PostMessageW")
	procGetModuleHandleW         = modkernel32.NewProc("GetModuleHandleW")
	procGetUserDefaultUILanguage = modkernel32.NewProc("GetUserDefaultUILanguage")
	procSetWindowLongPtrW        = moduser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW          = moduser32.NewProc("CallWindowProcW")
	procRegisterHotKey           = moduser32.NewProc("RegisterHotKey")
	procUnregisterHotKey         = moduser32.NewProc("UnregisterHotKey")
)

// GWLP_WNDPROC. A var, not a const: uintptr(int(gwlpWndProc)) needs to run
// as an actual truncating/sign-extending runtime conversion to land on the
// correct 0xFFFFFFFFFFFFFFFC bit pattern SetWindowLongPtrW expects — with
// a const operand Go evaluates the whole expression at compile time with
// infinite-precision arithmetic instead, where converting a negative value
// to an unsigned type is simply rejected as overflow.
var gwlpWndProc int32 = -4

var origMainWndProc uintptr

// hideToTrayOnClose subclasses the main WebView2 window so its title-bar
// close button (and Alt+F4 — both end up posting WM_CLOSE, including via
// DefWindowProc's own SC_CLOSE handling) hides the window instead of
// destroying it. go-webview2's own WndProc (wndproc in webview.go) reacts
// to WM_CLOSE by calling DestroyWindow — that's the one behavior being
// overridden here; everything else is forwarded unchanged to the original
// proc via CallWindowProcW. Only call this once a tray icon exists to
// reopen the window from — see its call site in webui.go.
func hideToTrayOnClose(hwnd uintptr) {
	newProc := windows.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
		if msg == wmClose {
			procShowWindow.Call(hwnd, swHide)
			return 0
		}
		r, _, _ := procCallWindowProcW.Call(origMainWndProc, hwnd, msg, wparam, lparam)
		return r
	})
	prev, _, _ := procSetWindowLongPtrW.Call(hwnd, uintptr(int(gwlpWndProc)), newProc)
	origMainWndProc = prev
}

const (
	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmCommand = 0x0111
	wmApp     = 0x8000
	// Distinct from go-webview2's own WM_APP (0x8000) use on its window —
	// this fires on a different hwnd/thread entirely, but +1 keeps it out
	// of the way regardless.
	wmAppTrayMsg = wmApp + 1
	// Posted (never sent directly) by pollTrayState to ask the tray's own
	// thread to run refreshHotkeys — RegisterHotKey/UnregisterHotKey are
	// thread-affine to whichever thread owns hwnd's message queue, which
	// is this window's dedicated goroutine, not pollTrayState's.
	wmRefreshHotkeys = wmApp + 2
	wmLButtonUp      = 0x0202
	wmLButtonDblClk  = 0x0203
	wmRButtonUp      = 0x0205

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	mfString    = 0x00000000
	mfGrayed    = 0x00000001
	mfSeparator = 0x00000800

	tpmRightAlign  = 0x0008
	tpmBottomAlign = 0x0020

	smCxSmIcon = 49

	trayMenuShow       = 1001
	trayMenuExit       = 1002
	trayMenuDisconnect = 1003
	// Profile entries get IDs starting here, one per list index
	// (trayMenuConnectBase + i) — see showMenu/wndProc's wmCommand case.
	trayMenuConnectBase = 2000

	langRussianPrimary = 0x19

	wmHotkey = 0x0312

	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modNoRepeat = 0x4000

	// Per-profile hotkey RegisterHotKey ids start here — see
	// refreshHotkeys. Kept out of the trayMenuConnectBase (2000+) range so
	// a stray wparam can't be misread as the other kind of id.
	trayHotkeyIDBase = 3000
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type point struct{ X, Y int32 }

type msgT struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// notifyIconDataW mirrors the modern (Vista+) NOTIFYICONDATAW layout.
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             windows.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     windows.Handle
}

type trayIcon struct {
	hwnd uintptr

	activeIcon   windows.Handle
	inactiveIcon windows.Handle

	mu          sync.Mutex
	currentIcon windows.Handle
	currentTip  string

	onShow func()
	onExit func()

	menuShowText    string
	menuExitText    string
	disconnectedTip string
	connectedTipFmt string

	// menuDisconnectFmt/menuNoProfiles are the quick-connect submenu's
	// text — see showMenu, which rebuilds it fresh (via fetchMenuState)
	// on every right-click rather than caching it.
	menuDisconnectFmt string
	menuNoProfiles    string

	// menuProfiles maps the dynamic profile menu-item IDs
	// (trayMenuConnectBase + index) back to a profile name — rebuilt by
	// showMenu each time it's opened, read by wndProc's wmCommand case.
	menuProfiles []string

	// hotkeyBindings maps a live RegisterHotKey id (trayHotkeyIDBase + n)
	// to the profile it toggles, and hotkeyStrings caches what each
	// profile's Hotkey field was last seen as ("" = registered id => name
	// entries only exist for non-empty hotkeys) — refreshHotkeys diffs
	// against hotkeyStrings each poll tick so an unchanged binding is
	// never needlessly unregistered/re-registered (that would open a
	// brief window where a fast keypress gets missed). All three fields
	// share t.mu with the icon/tip state above.
	hotkeyBindings map[int]string
	hotkeyStrings  map[string]string
	nextHotkeyID   int
}

func systemIsRussian() bool {
	langID, _, _ := procGetUserDefaultUILanguage.Call()
	return uint16(langID)&0x3ff == langRussianPrimary
}

func trayIconSize() int32 {
	r, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	if r == 0 {
		return 16
	}
	return int32(r)
}

func createIconFromPNG(png []byte, size int32) (windows.Handle, error) {
	if len(png) == 0 {
		return 0, fmt.Errorf("empty icon resource")
	}
	h, _, _ := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&png[0])),
		uintptr(len(png)),
		1,          // fIcon = TRUE
		0x00030000, // dwVer
		uintptr(size),
		uintptr(size),
		0, // LR_DEFAULTCOLOR
	)
	if h == 0 {
		return 0, fmt.Errorf("CreateIconFromResourceEx failed")
	}
	return windows.Handle(h), nil
}

func copyStringToBuf(buf []uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	n := len(u)
	if n > len(buf) {
		n = len(buf)
		buf[len(buf)-1] = 0
	}
	copy(buf, u[:n])
}

// newTrayIcon creates the tray icon and blocks until it's actually added
// (or failed) — callers get a definitive answer rather than racing a
// background goroutine's startup.
func newTrayIcon(onShow, onExit func()) (*trayIcon, error) {
	t := &trayIcon{
		onShow:         onShow,
		onExit:         onExit,
		hotkeyBindings: make(map[int]string),
		hotkeyStrings:  make(map[string]string),
		nextHotkeyID:   trayHotkeyIDBase,
	}
	if systemIsRussian() {
		t.menuShowText = "Открыть"
		t.menuExitText = "Выход"
		t.disconnectedTip = "Spectrune — Отключено"
		t.connectedTipFmt = "Spectrune — Подключено: %s"
		t.menuDisconnectFmt = "Отключить: %s"
		t.menuNoProfiles = "Нет сохранённых профилей"
	} else {
		t.menuShowText = "Open"
		t.menuExitText = "Exit"
		t.disconnectedTip = "Spectrune — Disconnected"
		t.connectedTipFmt = "Spectrune — Connected: %s"
		t.menuDisconnectFmt = "Disconnect: %s"
		t.menuNoProfiles = "No saved profiles"
	}

	ready := make(chan error, 1)

	go func() {
		runtime.LockOSThread()

		size := trayIconSize()
		activeIcon, err := createIconFromPNG(trayActivePNG, size)
		if err != nil {
			ready <- err
			return
		}
		inactiveIcon, err := createIconFromPNG(trayInactivePNG, size)
		if err != nil {
			ready <- err
			return
		}
		t.activeIcon = activeIcon
		t.inactiveIcon = inactiveIcon

		hInstance, _, _ := procGetModuleHandleW.Call(0)
		className, _ := windows.UTF16PtrFromString("SpectruneTrayWnd")

		var wc wndClassExW
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		wc.lpfnWndProc = windows.NewCallback(t.wndProc)
		wc.hInstance = windows.Handle(hInstance)
		wc.lpszClassName = className

		if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			ready <- fmt.Errorf("RegisterClassExW failed")
			return
		}

		emptyName, _ := windows.UTF16PtrFromString("")
		hwnd, _, _ := procCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(emptyName)),
			0,
			0, 0, 0, 0,
			0, 0,
			hInstance,
			0,
		)
		if hwnd == 0 {
			ready <- fmt.Errorf("CreateWindowExW failed")
			return
		}
		t.hwnd = hwnd

		// Per-profile hotkeys (each profile's own Hotkey field, set from
		// the edit view) are registered here, on this same thread —
		// RegisterHotKey delivers WM_HOTKEY to whichever thread's message
		// queue owns the associated window. Initial population; ongoing
		// changes are picked up by pollTrayState calling this again
		// alongside its icon-state poll.
		t.refreshHotkeys()

		var nid notifyIconDataW
		nid.cbSize = uint32(unsafe.Sizeof(nid))
		nid.hWnd = windows.Handle(hwnd)
		nid.uID = 1
		nid.uFlags = nifMessage | nifIcon | nifTip
		nid.uCallbackMessage = wmAppTrayMsg
		nid.hIcon = t.inactiveIcon
		t.currentIcon = t.inactiveIcon
		t.currentTip = t.disconnectedTip
		copyStringToBuf(nid.szTip[:], t.disconnectedTip)

		if r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
			ready <- fmt.Errorf("Shell_NotifyIconW(NIM_ADD) failed")
			return
		}

		ready <- nil

		var m msgT
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}

		var delNid notifyIconDataW
		delNid.cbSize = uint32(unsafe.Sizeof(delNid))
		delNid.hWnd = windows.Handle(t.hwnd)
		delNid.uID = 1
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&delNid)))
	}()

	if err := <-ready; err != nil {
		return nil, err
	}
	return t, nil
}

func (t *trayIcon) wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch uint32(msg) {
	case wmAppTrayMsg:
		switch uint32(lparam) {
		case wmLButtonUp, wmLButtonDblClk:
			if t.onShow != nil {
				t.onShow()
			}
		case wmRButtonUp:
			t.showMenu(hwnd)
		}
		return 0
	case wmCommand:
		id := uint32(wparam) & 0xffff
		switch {
		case id == trayMenuShow:
			if t.onShow != nil {
				t.onShow()
			}
		case id == trayMenuExit:
			if t.onExit != nil {
				t.onExit()
			}
		case id == trayMenuDisconnect:
			go t.quickDisconnect()
		case id >= trayMenuConnectBase:
			t.mu.Lock()
			idx := int(id - trayMenuConnectBase)
			var name string
			if idx >= 0 && idx < len(t.menuProfiles) {
				name = t.menuProfiles[idx]
			}
			t.mu.Unlock()
			if name != "" {
				go t.quickConnect(name)
			}
		}
		return 0
	case wmRefreshHotkeys:
		t.refreshHotkeys()
		return 0
	case wmHotkey:
		t.mu.Lock()
		name := t.hotkeyBindings[int(wparam)]
		t.mu.Unlock()
		if name != "" {
			go t.quickToggleProfile(name)
		}
		return 0
	case wmClose:
		t.mu.Lock()
		for id := range t.hotkeyBindings {
			procUnregisterHotKey.Call(hwnd, uintptr(id))
		}
		t.mu.Unlock()
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// showMenu builds the right-click menu fresh every time it's opened —
// current connection state and the profile list, straight from the
// service over IPC (fetchMenuState), rather than a cached/polled copy —
// so it's never showing stale profile names or the wrong Connect/
// Disconnect state. This is the "quick connect without opening the app"
// feature: when disconnected, every saved profile is a one-click Connect
// item; when connected, there's a single Disconnect item naming the
// active profile instead.
func (t *trayIcon) showMenu(hwnd uintptr) {
	profiles, connected, connectedProfile := t.fetchMenuState()

	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	t.mu.Lock()
	t.menuProfiles = nil
	t.mu.Unlock()

	switch {
	case connected:
		label, _ := windows.UTF16PtrFromString(fmt.Sprintf(t.menuDisconnectFmt, connectedProfile))
		procAppendMenuW.Call(hMenu, mfString, trayMenuDisconnect, uintptr(unsafe.Pointer(label)))
	case len(profiles) > 0:
		t.mu.Lock()
		t.menuProfiles = profiles
		t.mu.Unlock()
		for i, name := range profiles {
			namePtr, _ := windows.UTF16PtrFromString(name)
			procAppendMenuW.Call(hMenu, mfString, uintptr(trayMenuConnectBase+i), uintptr(unsafe.Pointer(namePtr)))
		}
	default:
		emptyLabel, _ := windows.UTF16PtrFromString(t.menuNoProfiles)
		procAppendMenuW.Call(hMenu, mfString|mfGrayed, 0, uintptr(unsafe.Pointer(emptyLabel)))
	}

	procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
	showText, _ := windows.UTF16PtrFromString(t.menuShowText)
	exitText, _ := windows.UTF16PtrFromString(t.menuExitText)
	procAppendMenuW.Call(hMenu, mfString, trayMenuShow, uintptr(unsafe.Pointer(showText)))
	procAppendMenuW.Call(hMenu, mfString, trayMenuExit, uintptr(unsafe.Pointer(exitText)))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// The classic Shell_NotifyIcon dance: the window must be foreground
	// before TrackPopupMenu or the menu won't dismiss on an outside click,
	// and a following harmless message nudges that closed reliably too.
	procSetForegroundWindow.Call(hwnd)
	procTrackPopupMenu.Call(hMenu, tpmRightAlign|tpmBottomAlign, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	procPostMessageW.Call(hwnd, 0, 0, 0)
}

// fetchMenuState dials the service directly (the tray never goes through
// the GUI/webview for this) for a fresh profile list + connection state.
// Failure just means an empty/disconnected menu, not an error dialog —
// this runs on a right-click, there's nowhere good to surface it.
func (t *trayIcon) fetchMenuState() (profiles []string, connected bool, connectedProfile string) {
	client, err := ipcDial()
	if err != nil {
		return nil, false, ""
	}
	defer client.Close()
	client.Call("Bridge.ListProfiles", struct{}{}, &profiles)
	var state StateReply
	client.Call("Bridge.State", struct{}{}, &state)
	return profiles, state.Connected, state.ProfileName
}

func (t *trayIcon) quickConnect(name string) {
	client, err := ipcDial()
	if err != nil {
		return
	}
	defer client.Close()
	if err := client.Call("Bridge.Connect", name, &struct{}{}); err != nil {
		log.Printf("tray quickConnect(%q): %v", name, err)
	}
}

func (t *trayIcon) quickDisconnect() {
	client, err := ipcDial()
	if err != nil {
		return
	}
	defer client.Close()
	if err := client.Call("Bridge.Disconnect", struct{}{}, &struct{}{}); err != nil {
		log.Printf("tray quickDisconnect: %v", err)
	}
}

// quickToggleProfile is a per-profile hotkey's action: Disconnect if name
// is the one currently connected, otherwise Connect to it (switching away
// from whatever else might be active — only one tunnel ever runs at a
// time in this app).
func (t *trayIcon) quickToggleProfile(name string) {
	client, err := ipcDial()
	if err != nil {
		return
	}
	defer client.Close()

	var state StateReply
	if err := client.Call("Bridge.State", struct{}{}, &state); err != nil {
		log.Printf("tray quickToggleProfile(%q): State: %v", name, err)
		return
	}
	if state.Connected && state.ProfileName == name {
		if err := client.Call("Bridge.Disconnect", struct{}{}, &struct{}{}); err != nil {
			log.Printf("tray quickToggleProfile(%q): Disconnect: %v", name, err)
		}
		return
	}
	if state.Connected {
		// Bridge.Connect refuses while another profile is active (see
		// service.go) — disconnect first so switching profiles by hotkey
		// works instead of just erroring.
		if err := client.Call("Bridge.Disconnect", struct{}{}, &struct{}{}); err != nil {
			log.Printf("tray quickToggleProfile(%q): Disconnect (switching): %v", name, err)
			return
		}
	}
	if err := client.Call("Bridge.Connect", name, &struct{}{}); err != nil {
		log.Printf("tray quickToggleProfile(%q): Connect: %v", name, err)
	}
}

// refreshHotkeys fetches every saved profile's Hotkey field and brings
// this process's live RegisterHotKey bindings in line with it — called
// once at tray startup and then every pollTrayState tick. Diffs against
// hotkeyStrings (what was registered last time) so an unchanged binding
// is left alone rather than unregistered and immediately re-registered,
// which would open a brief window where a fast keypress lands while
// nothing is bound. Must run on the tray's own thread — RegisterHotKey/
// UnregisterHotKey apply to the calling thread's message queue, and
// that's the thread newTrayIcon's message loop actually pumps.
func (t *trayIcon) refreshHotkeys() {
	client, err := ipcDial()
	if err != nil {
		return
	}
	defer client.Close()

	var names []string
	if err := client.Call("Bridge.ListProfiles", struct{}{}, &names); err != nil {
		return
	}

	current := make(map[string]string, len(names)) // profile name -> hotkey string
	for _, name := range names {
		var cfg conf.Config
		if err := client.Call("Bridge.LoadProfile", name, &cfg); err != nil {
			continue
		}
		if cfg.Interface.Hotkey != "" {
			current[name] = cfg.Interface.Hotkey
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Unregister anything removed or changed.
	for id, name := range t.hotkeyBindings {
		if current[name] != t.hotkeyStrings[name] {
			procUnregisterHotKey.Call(t.hwnd, uintptr(id))
			delete(t.hotkeyBindings, id)
			delete(t.hotkeyStrings, name)
		}
	}

	// Register anything new or changed.
	for name, hk := range current {
		if t.hotkeyStrings[name] == hk {
			continue // unchanged, still bound from a previous pass
		}
		mods, vk, ok := parseHotkeyString(hk)
		if !ok {
			log.Printf("refreshHotkeys: profile %q has unparseable hotkey %q", name, hk)
			continue
		}
		id := t.nextHotkeyID
		t.nextHotkeyID++
		if r, _, _ := procRegisterHotKey.Call(t.hwnd, uintptr(id), uintptr(mods|modNoRepeat), uintptr(vk)); r == 0 {
			log.Printf("refreshHotkeys: RegisterHotKey(%q) for profile %q failed — probably already bound by another app", hk, name)
			continue
		}
		t.hotkeyBindings[id] = name
		t.hotkeyStrings[name] = hk
	}
}

// parseHotkeyString parses the "Ctrl+Alt+F1" form the edit view's hotkey
// capture widget produces (webui_html.go) into RegisterHotKey's modifier
// bitmask + virtual-key code. At least one modifier is required — the
// capture widget itself enforces that, but a hand-edited config could
// still contain a bare key.
func parseHotkeyString(s string) (mods uint32, vk uint32, ok bool) {
	parts := strings.Split(s, "+")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch strings.ToLower(p) {
		case "ctrl", "control":
			mods |= modControl
		case "alt":
			mods |= modAlt
		case "shift":
			mods |= modShift
		default:
			if vk != 0 {
				return 0, 0, false // more than one non-modifier part
			}
			vk, ok = vkFromKeyName(p)
			if !ok {
				return 0, 0, false
			}
		}
	}
	if vk == 0 || mods == 0 {
		return 0, 0, false
	}
	return mods, vk, true
}

// vkFromKeyName covers exactly what the JS capture widget can produce:
// A-Z, 0-9, and F1-F12.
func vkFromKeyName(name string) (uint32, bool) {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch {
	case len(name) == 1 && name[0] >= 'A' && name[0] <= 'Z':
		return uint32(name[0]), true
	case len(name) == 1 && name[0] >= '0' && name[0] <= '9':
		return uint32(name[0]), true
	case len(name) >= 2 && len(name) <= 3 && name[0] == 'F':
		n, err := strconv.Atoi(name[1:])
		if err == nil && n >= 1 && n <= 12 {
			return uint32(0x6F + n), true // VK_F1 = 0x70
		}
	}
	return 0, false
}

// SetState updates the tray glyph + tooltip to match live connection
// state. Safe to call from any goroutine — Shell_NotifyIcon itself is,
// and the mutex just avoids redundant/interleaved calls when polled.
func (t *trayIcon) SetState(connected bool, profileName string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	icon := t.inactiveIcon
	tip := t.disconnectedTip
	if connected {
		icon = t.activeIcon
		tip = fmt.Sprintf(t.connectedTipFmt, profileName)
	}
	if icon == t.currentIcon && tip == t.currentTip {
		return
	}
	t.currentIcon = icon
	t.currentTip = tip

	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = windows.Handle(t.hwnd)
	nid.uID = 1
	nid.uFlags = nifIcon | nifTip
	nid.hIcon = icon
	copyStringToBuf(nid.szTip[:], tip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// Close removes the tray icon and stops its message loop. Posts WM_CLOSE
// rather than calling DestroyWindow directly — the window is owned by a
// different OS thread (the one running newTrayIcon's loop), and Win32
// window calls must happen on the owning thread.
func (t *trayIcon) Close() {
	if t.hwnd != 0 {
		procPostMessageW.Call(t.hwnd, wmClose, 0, 0)
	}
}

// PostRefreshHotkeys asks the tray's own thread to re-sync its
// RegisterHotKey bindings against every profile's current Hotkey field —
// safe to call from any goroutine, unlike refreshHotkeys itself (see
// wmRefreshHotkeys' doc).
func (t *trayIcon) PostRefreshHotkeys() {
	if t.hwnd != 0 {
		procPostMessageW.Call(t.hwnd, wmRefreshHotkeys, 0, 0)
	}
}

// pollTrayState keeps the tray icon's color in sync with the service's
// actual connection state (polled over the same IPC the CLI/GUI already
// use) until stop is closed. A short interval keeps the glyph responsive
// to Connect/Disconnect without adding a second push-notification path.
func pollTrayState(tray *trayIcon, stop <-chan struct{}) {
	poll := func() {
		client, err := ipcDial()
		if err != nil {
			tray.SetState(false, "")
			return
		}
		defer client.Close()
		var state StateReply
		if err := client.Call("Bridge.State", struct{}{}, &state); err != nil {
			tray.SetState(false, "")
			return
		}
		// The glyph only goes colored once the handshake actually
		// succeeded, not just because the local adapter came up — see
		// StateReply.HandshakeOK's doc.
		tray.SetState(state.Connected && state.HandshakeOK, state.ProfileName)
		tray.PostRefreshHotkeys()
	}

	poll()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			poll()
		}
	}
}
