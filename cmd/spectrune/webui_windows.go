// Main GUI window, built on WebView2 (github.com/jchv/go-webview2)
// instead of lxn/walk — walk's widget init hit a confirmed, unresolved
// TTM_ADDTOOL failure on this machine (see git history / session notes for
// the full investigation) that WebView2 sidesteps entirely: it never
// touches comctl32 tooltips, and opened cleanly on the very first try in
// testing 2026-09-03. The window is a single HTML page (below); all
// app logic still goes through the same SpectruneService IPC (ipc.go)
// the CLI subcommands in main.go already use — only the front end changed.
package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/amnezia-vpn/amneziawg-windows/v3/conf"
)

var (
	modkernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleWindow = modkernel32.NewProc("GetConsoleWindow")
	moduser32            = windows.NewLazySystemDLL("user32.dll")
	procShowWindow       = moduser32.NewProc("ShowWindow")
	procLoadImageW       = moduser32.NewProc("LoadImageW")
	procSendMessageW     = moduser32.NewProc("SendMessageW")
	procFindWindowW      = moduser32.NewProc("FindWindowW")
)

// guiWindowTitle must match WindowOptions.Title below exactly — it's how
// a second /gui launch finds the already-running window to activate (see
// activateExistingGUIWindow).
const guiWindowTitle = "Spectrune"

// activateExistingGUIWindow finds an already-running GUI's window by title
// and brings it to front, including un-hiding it if it was closed to tray
// (hideToTrayOnClose only ever calls ShowWindow(SW_HIDE), so the window
// still exists and is still findable — it's just invisible). Returns false
// if no such window exists.
func activateExistingGUIWindow() bool {
	titlePtr, _ := windows.UTF16PtrFromString(guiWindowTitle)
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return false
	}
	procShowWindow.Call(hwnd, swRestore)
	procSetForegroundWindow.Call(hwnd)
	return true
}

const (
	swHide    = 0
	swRestore = 9

	imageIcon      = 1
	lrDefaultColor = 0x0000
	wmSetIcon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
)

// setWindowIconFromResource sets hwnd's title-bar/Alt-Tab icon to icon
// resource 1 (the branded "A" mark embedded via resources.rc → app.ico —
// see the ICON line there). go-webview2 registers its own window class
// with no icon of its own, so without this the title bar shows a blank/
// generic icon even though the taskbar and Explorer already pick up the
// exe's resource icon correctly (that comes from a different, unrelated
// lookup — the shell's file-association icon, not the live window's).
// Loaded twice, at the two sizes Windows actually asks for (big: Alt-Tab/
// taskbar thumbnail, small: title bar), rather than one size stretched.
func setWindowIconFromResource(hwnd uintptr) {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	if big, _, _ := procLoadImageW.Call(hInstance, 1, imageIcon, 32, 32, lrDefaultColor); big != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, big)
	}
	if small, _, _ := procLoadImageW.Call(hInstance, 1, imageIcon, 16, 16, lrDefaultColor); small != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	}
}

// ProfileDetails is what loadProfile hands to the JS side — see its call
// site below for why this is pre-formatted text rather than the raw
// conf.Config.
type ProfileDetails struct {
	ConfigText   string
	IncludedApps []string
	// IncludedDomainLists always comes back empty here — domain-list
	// routing (domainlists_linux.go) is a Linux-only feature, built on
	// cgroup/ipset mechanisms Windows' WFP-based per-app interception
	// doesn't have an equivalent of. Kept on this struct only so the
	// shared webui_html.go JS (which reads/writes this field
	// unconditionally) doesn't need an OS feature-detect for it.
	IncludedDomainLists []string
	AutoConnect         bool
	Hotkey              string
}

// hideConsoleWindow hides this process's own console window. Spectrune
// is built as a plain console-subsystem exe (not -H windowsgui) on
// purpose, so the CLI subcommands in main.go (/status, /list, …) still
// print visibly — but that means double-clicking the /gui shortcut also
// briefly flashes/leaves up a console window behind the real UI. Hiding
// it here, right as the GUI starts, keeps both: console output still
// works for anyone piping/redirecting `spectrune.exe /gui`, but a
// normal interactive launch only shows the WebView2 window.
func hideConsoleWindow() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(hwnd, swHide)
}

// writeUIHTMLFile writes html to a stable, per-user path — the same path
// every launch, which is what actually matters for file:// storage
// partitioning (see the Navigate call site's comment). %LocalAppData% is
// always writable by the logged-in user without elevation, unlike next
// to the exe itself once that's installed under Program Files.
func writeUIHTMLFile(html string) (string, error) {
	dir, err := os.UserCacheDir() // %LocalAppData% on Windows
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "Spectrune")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "ui.html")
	if err := os.WriteFile(path, []byte(html), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func runGUI() {
	// Single-instance guard: the desktop shortcut runs "spectrune.exe /gui"
	// (installer/spectrune.wxs), and nothing before this stopped a second
	// launch — each one opened its own WebView2 window *and* its own tray
	// icon alongside the first, confirmed live 2026-09-06. A distinct
	// mutex from runLegacyDirect's Global\SpectruneSingleInstance (main.go)
	// since that one guards the old single-tunnel companion mode, not this
	// GUI. On a second launch, activate the existing window instead of
	// just exiting silently — otherwise clicking the shortcut again while
	// the app is minimized to tray looks like it did nothing.
	mutexName, _ := windows.UTF16PtrFromString(`Global\SpectruneGUISingleInstance`)
	mutex, mutexErr := windows.CreateMutex(nil, false, mutexName)
	if mutexErr == windows.ERROR_ALREADY_EXISTS {
		if !activateExistingGUIWindow() {
			log.Printf("another Spectrune GUI instance is already running but its window wasn't found — exiting anyway")
		}
		return
	}
	if mutex != 0 {
		defer windows.CloseHandle(mutex)
	}

	hideConsoleWindow()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  guiWindowTitle,
			Width:  560,
			Height: 640,
			Center: true,
		},
	})
	if w == nil {
		log.Fatal("failed to initialize WebView2 — is the WebView2 Runtime installed? (bundled with Windows 10 1809+/11 by default)")
	}
	defer w.Destroy()

	bindAPI(w)
	go checkForUpdateOnLaunch()

	hwnd := uintptr(w.Window())
	setWindowIconFromResource(hwnd)

	tray, err := newTrayIcon(
		func() {
			// Runs on the tray's own thread — Dispatch marshals the actual
			// Win32 calls onto the WebView2 window's thread, same rule
			// Terminate() below follows (see tray.go's doc comment).
			w.Dispatch(func() {
				procShowWindow.Call(hwnd, swRestore)
				procSetForegroundWindow.Call(hwnd)
			})
		},
		func() {
			// DestroyWindow, not w.Terminate() directly — Terminate() just
			// posts WM_QUIT, which ends Run()'s message loop immediately
			// without the WebView2 controller/environment ever going
			// through a real WM_DESTROY. Chromium's storage backend
			// (leveldb, backing localStorage — see webui_html.go's theme
			// persistence) flushes pending writes on that normal shutdown
			// path; skipping it left a chosen background photo or theme
			// change unsaved if Exit was clicked before Windows' own
			// periodic flush happened to run. DestroyWindow sends
			// WM_DESTROY synchronously to whichever wndproc is currently
			// installed — hideToTrayOnClose's subclass only intercepts
			// WM_CLOSE, so WM_DESTROY passes through to go-webview2's own
			// wndproc unchanged, which calls Terminate() itself as part of
			// its normal handling (see its WMDestroy case) — this just
			// lets that real chain run instead of shortcutting past it.
			w.Dispatch(func() { procDestroyWindow.Call(hwnd) })
		},
	)
	if err != nil {
		// No tray icon means no way to reopen a hidden window, so leave
		// the close button's default behavior (actually exit) alone —
		// trapping the user with an unreachable window would be worse.
		log.Printf("tray icon unavailable: %v", err)
	} else {
		hideToTrayOnClose(hwnd)
		stopPoll := make(chan struct{})
		go pollTrayState(tray, stopPoll)
		defer tray.Close()
		defer close(stopPoll)
	}

	// Navigate to a real file:// URL instead of SetHtml (NavigateToString)
	// — confirmed live 2026-09-04: content loaded via NavigateToString
	// doesn't get a stable persistent storage partition in WebView2/
	// Chromium, so localStorage (webui_html.go's theme/background-photo/
	// language persistence) worked fine within one running session but
	// reset on every restart regardless of shutdown timing — not a quota
	// or flush-timing problem (a small value like the theme accent choice
	// was lost exactly like a multi-hundred-KB background photo data URI,
	// which ruled that out). file:// URLs get a real, stable per-path
	// storage partition, so writing the page out once per launch and
	// navigating to it fixes this without needing WebView2 APIs
	// go-webview2 doesn't expose (like VirtualHostNameToFolderMapping).
	htmlPath, err := writeUIHTMLFile(strings.Replace(webUIHTML, "%%VERSION%%", appVersion, 1))
	if err != nil {
		log.Printf("writeUIHTMLFile failed, falling back to SetHtml (theme/background settings won't persist across restarts): %v", err)
		w.SetHtml(strings.Replace(webUIHTML, "%%VERSION%%", appVersion, 1))
	} else {
		fileURL := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(htmlPath)}
		w.Navigate(fileURL.String())
	}
	w.Run()

	// Run() only returns once the window itself is gone (WM_DESTROY ran —
	// see the tray Exit callback's comment above), but the WebView2
	// browser process's own leveldb flush for localStorage is a separate,
	// IPC-driven async step on its side that DestroyWindow doesn't wait
	// on. A short grace period here before the process actually exits
	// gives that a real chance to finish instead of racing it.
	time.Sleep(300 * time.Millisecond)
}

// bindAPI exposes Go functions as window.<name>(...) promises in the page
// (see jchv/go-webview2's Bind doc — each call is proxied over the
// WebView2 message channel and JSON-marshaled both ways). Every handler
// here just forwards to the service over the same IPC used by the CLI.
func bindAPI(w webview2.WebView) {
	must := func(err error) {
		if err != nil {
			log.Fatalf("Bind: %v", err)
		}
	}

	must(w.Bind("listProfiles", func() ([]string, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var names []string
		if err := client.Call("Bridge.ListProfiles", struct{}{}, &names); err != nil {
			return nil, err
		}
		return names, nil
	}))

	must(w.Bind("loadProfile", func(name string) (*ProfileDetails, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var cfg conf.Config
		if err := client.Call("Bridge.LoadProfile", name, &cfg); err != nil {
			return nil, err
		}
		// Hand back cfg.ToWgQuick()'s already-correct text rather than the
		// raw conf.Config struct: net.IP is a []byte under the hood, and
		// whatever JSON path go-webview2's Bind uses turns that into a
		// bare array of numbers on the JS side ([10,4,0,3], not a dotted
		// string) — reconstructing wg-quick text from that in JS produced
		// literal "10,4,0,3/32" garbage. ToWgQuick() is the same
		// serializer every other save path in this app already trusts.
		return &ProfileDetails{
			ConfigText:   cfg.ToWgQuick(),
			IncludedApps: cfg.Interface.IncludedApps,
			AutoConnect:  cfg.Interface.AutoConnect,
			Hotkey:       cfg.Interface.Hotkey,
		}, nil
	}))

	// includedDomainLists is accepted (to match the shared webui_html.go
	// JS's call signature) and discarded — see ProfileDetails' doc comment
	// above on why this feature has no Windows equivalent.
	must(w.Bind("saveProfile", func(name, wgQuickText string, includedApps []string, includedDomainLists []string, autoConnect bool, hotkey string) error {
		if !profileNameIsValid(name) {
			return fmt.Errorf("profile name %q is not valid", name)
		}
		// FromWgQuick's own name check is ASCII-only (conf.TunnelNameIsValid)
		// and doesn't apply to Spectrune profile names — see
		// profileNameIsValid's doc in profiles.go. Parse under a name
		// guaranteed to pass it, then restore the real one right after.
		cfg, err := conf.FromWgQuick(wgQuickText, placeholderTunnelName)
		if err != nil {
			return err
		}
		cfg.Name = name
		cfg.Interface.IncludedApps = includedApps
		cfg.Interface.AutoConnect = autoConnect
		cfg.Interface.Hotkey = hotkey
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.SaveProfile", *cfg, &struct{}{})
	}))

	must(w.Bind("deleteProfile", func(name string) error {
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.DeleteProfile", name, &struct{}{})
	}))

	must(w.Bind("renameProfile", func(oldName, newName string) error {
		if !profileNameIsValid(newName) {
			return fmt.Errorf("profile name %q is not valid", newName)
		}
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.RenameProfile", RenameProfileRequest{OldName: oldName, NewName: newName}, &struct{}{})
	}))

	must(w.Bind("connect", func(name string) error {
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.Connect", name, &struct{}{})
	}))

	must(w.Bind("disconnect", func() error {
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.Disconnect", struct{}{}, &struct{}{})
	}))

	must(w.Bind("getState", func() (*StateReply, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var state StateReply
		if err := client.Call("Bridge.State", struct{}{}, &state); err != nil {
			return nil, err
		}
		return &state, nil
	}))

	must(w.Bind("listInstalledApps", func() ([]installedApp, error) {
		return enumerateInstalledApps(), nil
	}))

	must(w.Bind("getAppIcon", func(path string) (string, error) {
		return getAppIconDataURI(path)
	}))

	must(w.Bind("browseForExe", func() (string, error) {
		return browseForExeFile()
	}))

	must(w.Bind("chooseBackgroundImage", func() (string, error) {
		return chooseBackgroundImage()
	}))

	must(w.Bind("listAppPresets", func() ([]string, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var names []string
		if err := client.Call("Bridge.ListAppPresets", struct{}{}, &names); err != nil {
			return nil, err
		}
		return names, nil
	}))

	must(w.Bind("saveAppPreset", func(name string, apps []string) error {
		if !profileNameIsValid(name) {
			return fmt.Errorf("preset name %q is not valid", name)
		}
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.SaveAppPreset", AppPresetSaveRequest{Name: name, Apps: apps}, &struct{}{})
	}))

	must(w.Bind("loadAppPreset", func(name string) ([]string, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var apps []string
		if err := client.Call("Bridge.LoadAppPreset", name, &apps); err != nil {
			return nil, err
		}
		return apps, nil
	}))

	must(w.Bind("deleteAppPreset", func(name string) error {
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.DeleteAppPreset", name, &struct{}{})
	}))

	// importConfig lets the user pick an existing .conf file (e.g.
	// exported from the AmneziaWG app) and returns its raw text so the
	// edit view can drop it straight into the textarea — no separate
	// "import" plumbing on the service side, this is purely a file-read
	// convenience for the same Save flow that already parses pasted text.
	must(w.Bind("importConfig", func() (string, error) {
		path, err := browseForFile("Import configuration", "Configuration files (*.conf)", "*.conf")
		if err != nil || path == "" {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}))

	// getAutostartEnabled/setAutostartEnabled back the "Launch on Windows
	// startup" toggle in webui_html.go's settings panel — see
	// autostart_windows.go. No Linux equivalent (feature-detected there
	// via window.getAutostartEnabled), so this pair only exists here.
	must(w.Bind("getAutostartEnabled", func() (bool, error) {
		return isAutostartEnabled()
	}))
	must(w.Bind("setAutostartEnabled", func(enable bool) error {
		return setAutostartEnabled(enable)
	}))
}
