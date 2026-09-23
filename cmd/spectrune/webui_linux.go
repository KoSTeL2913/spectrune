// Main GUI window for Linux, built on github.com/webview/webview_go
// (GTK+WebKitGTK under the hood) — the Linux analog of webui_windows.go's
// WebView2 window. Reuses the exact same webUIHTML content (webui_html.go)
// as the Windows build: same theming/RGB customization/profile list
// markup, only the Go-side bindings differ per OS.
//
// Build note: webview_go's cgo directives hardcode a "webkit2gtk-4.0"
// pkg-config name that no longer exists on this machine (Ubuntu 24.04+
// only ships webkit2gtk-4.1, API-compatible but renamed) — build with
// PKG_CONFIG_PATH pointing at a directory containing webkit2gtk-4.0.pc/
// javascriptcoregtk-4.0.pc shim files whose Requires: line points at the
// real 4.1 packages. Confirmed working 2026-09-08; see
// ~/.claude/plans/idempotent-nibbling-hinton.md for the shim file
// contents if they need recreating.
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/webview/webview_go"
)

const guiWindowTitle = "Spectrune"

// ProfileDetails mirrors webui_windows.go's — same JSON shape the shared
// webUIHTML JS already expects.
type ProfileDetails struct {
	ConfigText          string
	IncludedApps        []string
	IncludedDomainLists []string
	AutoConnect         bool
	Hotkey              string
}

// installedApp mirrors installedapps_windows.go's shape ({Name, Path}) —
// Path holds the .desktop entry ID here instead of an exe path, since
// that's what LaunchApp (apps_linux.go) and a profile's IncludedApps need
// to identify an app on Linux.
type installedApp struct {
	Name string
	Path string
}

// writeUIHTMLFile writes html to a stable per-user path, matching
// webui_windows.go's same reasoning: a real file:// URL gets a stable,
// persistent WebKitGTK storage partition, unlike an in-memory SetHtml
// load — needed for the theme/background-photo persistence
// webui_html.go's JS already relies on (proven on Windows; not re-verified
// against WebKitGTK's own storage model yet, so watch for the same
// "settings reset on restart" symptom if it recurs here).
func writeUIHTMLFile(html string) (string, error) {
	dir, err := os.UserConfigDir() // ~/.config
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "spectrune")
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
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle(guiWindowTitle)
	w.SetSize(560, 640, webview.HintNone)

	bindAPI(w)
	startHotkeyManager()
	go checkForUpdateOnLaunch()
	// checkForUpdateOnLaunch above is fire-and-forget by design (never
	// reports back whether it found/installed anything, so a routine
	// launch never blocks on network I/O) — the GUI's own
	// checkBackgroundUpdate loop (webui_html.go), polling
	// Bridge.UpdateInProgress every 3s regardless of what triggered an
	// install, is what notices this one starting and handles closing
	// and relaunching the window. An earlier version of this file also
	// ran a second, independent Go-side poll-and-restart loop here as a
	// safety net for this exact silent path — removed 2026-09-14 after
	// it and the JS-side mechanism raced each other into spawning two
	// replacement windows for the same update (confirmed live: two
	// overlapping Spectrune windows after one install). One mechanism,
	// not two.

	hwin := w.Window()
	// 613x357 is the user's own hands-on minimum (2026-09-23): shrunk the
	// window down live until the main list's button row was about to
	// clip/wrap (width) and the connected profile card was about to get
	// squeezed away (height), then asked for that exact size locked in
	// as the floor — after JS-side attempts to *compute* the width part
	// (summing button widths, then scrollWidth with flex-wrap: nowrap)
	// each came up short by the same ~50px for reasons never fully
	// pinned down.
	setMinSize(hwin, 613, 357)

	tray, err := newTrayIcon(w,
		func() { w.Dispatch(func() { showWindow(hwin) }) },
		func() { w.Dispatch(func() { w.Terminate() }) },
	)
	var stopPoll chan struct{}
	if err != nil {
		// No tray icon means no way to reopen a hidden window — same
		// reasoning as webui_windows.go's identical fallback: leave the
		// close button's default (real exit) alone rather than trapping
		// the user with an unreachable window, so hideToTrayOnClose is
		// deliberately NOT called in this branch.
		fmt.Fprintf(os.Stderr, "tray icon unavailable: %v\n", err)
	} else {
		hideToTrayOnClose(hwin)
		stopPoll = make(chan struct{})
		go pollTrayState(tray, stopPoll)
	}

	htmlPath, err := writeUIHTMLFile(strings.ReplaceAll(webUIHTML, "%%VERSION%%", appVersion))
	if err != nil {
		fmt.Fprintf(os.Stderr, "writeUIHTMLFile failed, falling back to SetHtml (theme/background settings won't persist across restarts): %v\n", err)
		w.SetHtml(strings.ReplaceAll(webUIHTML, "%%VERSION%%", appVersion))
	} else {
		w.Navigate("file://" + htmlPath)
	}
	w.Run()
	if stopPoll != nil {
		close(stopPoll)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Bind: %v\n", err)
		os.Exit(1)
	}
}

func bindAPI(w webview.WebView) {
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
		var cfg LinuxConfig
		if err := client.Call("Bridge.LoadProfile", name, &cfg); err != nil {
			return nil, err
		}
		return &ProfileDetails{
			ConfigText:          cfg.ToWgQuick(),
			IncludedApps:        cfg.IncludedApps,
			IncludedDomainLists: cfg.IncludedDomainLists,
			AutoConnect:         cfg.AutoConnect,
			Hotkey:              cfg.Hotkey,
		}, nil
	}))

	must(w.Bind("saveProfile", func(name, wgQuickText string, includedApps []string, includedDomainLists []string, autoConnect bool, hotkey string) error {
		if !profileNameIsValid(name) {
			return fmt.Errorf("profile name %q is not valid", name)
		}
		cfg, err := ParseWgQuick(wgQuickText, name)
		if err != nil {
			return err
		}
		cfg.IncludedApps = includedApps
		cfg.IncludedDomainLists = includedDomainLists
		cfg.AutoConnect = autoConnect
		cfg.Hotkey = hotkey
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

	// checkForUpdateNow backs the "Check for updates" button in
	// webui_html.go's settings panel — see update_linux.go's
	// CheckForUpdateNow (bypasses the once-per-launch throttle, reports
	// back immediately, installs in the background if newer).
	must(w.Bind("checkForUpdateNow", func() (*UpdateCheckReply, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var reply UpdateCheckReply
		if err := client.Call("Bridge.CheckForUpdateNow", struct{}{}, &reply); err != nil {
			return nil, err
		}
		return &reply, nil
	}))

	// getUpdateInProgress backs the "installing update" overlay
	// (webui_html.go's checkBackgroundUpdate, polled continuously in the
	// background, not just after a manual "Check for updates" click) —
	// a connection failure here is treated the same as "not installing"
	// by the JS side, since by the time the daemon is actually
	// unreachable (mid dpkg -i) the overlay should already be showing
	// from the last successful poll that caught updateInProgress==true
	// during the download phase.
	must(w.Bind("getUpdateInProgress", func() (bool, error) {
		client, err := ipcDial()
		if err != nil {
			return false, err
		}
		defer client.Close()
		var installing bool
		if err := client.Call("Bridge.UpdateInProgress", struct{}{}, &installing); err != nil {
			return false, err
		}
		return installing, nil
	}))

	// getRunningVersion backs checkBackgroundUpdate's fallback check —
	// on a small/fast package or a fast connection, the whole download+
	// dpkg -i+restart cycle can finish in well under one 3s poll
	// interval, so a poll can land entirely between two ticks and never
	// once observe updateInProgress==true (confirmed live 2026-09-14:
	// the window sat on a stale connection-error banner afterward,
	// having missed the only chance to notice). Comparing the daemon's
	// actual reported version against what this page loaded with
	// catches that case too, independent of whether the in-progress
	// flag was ever caught.
	must(w.Bind("getRunningVersion", func() (string, error) {
		client, err := ipcDial()
		if err != nil {
			return "", err
		}
		defer client.Close()
		var version string
		if err := client.Call("Bridge.Version", struct{}{}, &version); err != nil {
			return "", err
		}
		return version, nil
	}))

	// closeForUpdate actually closes this window once an install has
	// started or finished (webui_html.go's checkBackgroundUpdate) —
	// staying open for the whole dpkg -i run used to leave a stale
	// window sitting on top of the daemon restarting underneath it
	// (reported live 2026-09-14: the window plainly did not close during
	// install).
	//
	// alreadyDone is checkBackgroundUpdate's fallback path (JS side): the
	// daemon's version has already been observed to differ from this
	// page's own, meaning the install is confirmed finished before this
	// was ever called — so this launches the plain, always-supported
	// /gui subcommand directly rather than /wait-and-reopen, no polling
	// needed. That distinction matters for more than just speed: when
	// !alreadyDone, waitAndReopen re-execs whatever's on disk at this
	// exact path, and on a fast install dpkg can replace that file
	// within under two seconds of the caller's very first sight of
	// updateInProgress==true — spawning it too late (a delay briefly
	// tried *before* the spawn, reverted 2026-09-14) risked losing that
	// race and re-exec'ing an already-replaced binary that might not
	// even recognize /wait-and-reopen. Confirmed live: exactly that,
	// printing a plain usage error instead of ever polling for anything,
	// silently leaving no GUI running at all afterward. So delayMs below
	// only ever pushes out the window-close itself (for the "installing"
	// overlay to be visible for a moment), never the spawn.
	must(w.Bind("closeForUpdate", func(delayMs int, alreadyDone bool) error {
		exePath, err := os.Executable()
		if err != nil {
			return err
		}
		relaunchArg := "/wait-and-reopen"
		if alreadyDone {
			relaunchArg = "/gui"
		}
		if err := exec.Command(exePath, relaunchArg).Start(); err != nil {
			return err
		}
		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}
		w.Dispatch(func() { w.Terminate() })
		return nil
	}))

	// listInstalledApps / a picked app's "Path" both go through the
	// daemon (ListApps/LaunchApp) rather than a local, unprivileged scan —
	// unlike Windows' registry-based enumeration, .desktop scanning itself
	// needs no privilege, but launching one into the tunnel's namespace
	// does, so both are exposed the same way for consistency.
	must(w.Bind("listInstalledApps", func() ([]installedApp, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var apps []DesktopApp
		if err := client.Call("Bridge.ListApps", struct{}{}, &apps); err != nil {
			return nil, err
		}
		out := make([]installedApp, len(apps))
		for i, a := range apps {
			out[i] = installedApp{Name: a.Name, Path: a.ID}
		}
		return out, nil
	}))

	// launchApp is the button webui_html.go's Apps view renders per row
	// (feature-detected there via `if (window.launchApp)`, since Windows
	// has no such binding at all — it matches an already-running
	// process's live traffic instead of launching a fresh one). This was
	// missing entirely until 2026-09-09: LaunchApp existed and worked
	// server-side (exercised plenty over the CLI), but nothing in the GUI
	// ever called it — clicking an app row only toggled its checkbox.
	// Confirmed live: a user report of "routing doesn't work" traced back
	// to checking their own already-running, completely unrelated browser
	// window, because the "Apps" view had never actually launched
	// anything in the first place.
	must(w.Bind("launchApp", func(desktopID string) error {
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		req := LaunchAppRequest{
			DesktopID: desktopID,
			Env: map[string]string{
				"DISPLAY":                  os.Getenv("DISPLAY"),
				"WAYLAND_DISPLAY":          os.Getenv("WAYLAND_DISPLAY"),
				"XDG_RUNTIME_DIR":          os.Getenv("XDG_RUNTIME_DIR"),
				"DBUS_SESSION_BUS_ADDRESS": os.Getenv("DBUS_SESSION_BUS_ADDRESS"),
				"_CALLER_UID":              fmt.Sprint(os.Getuid()),
				"_CALLER_GID":              fmt.Sprint(os.Getgid()),
			},
		}
		return client.Call("Bridge.LaunchApp", req, &struct{}{})
	}))

	must(w.Bind("getAppIcon", func(path string) (string, error) {
		client, err := ipcDial()
		if err != nil {
			return "", err
		}
		defer client.Close()
		var iconValue string
		if err := client.Call("Bridge.AppIconValue", path, &iconValue); err != nil {
			return "", err
		}
		return getAppIconDataURI(iconValue)
	}))

	must(w.Bind("browseForExe", func() (string, error) {
		return zenityFilePicker("Select an application")
	}))

	must(w.Bind("chooseBackgroundImage", func() (string, error) {
		path, err := zenityFilePicker("Choose a background image")
		if err != nil || path == "" {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return imageToDataURI(path, data)
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

	must(w.Bind("listDomainLists", func() ([]string, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var names []string
		if err := client.Call("Bridge.ListDomainLists", struct{}{}, &names); err != nil {
			return nil, err
		}
		return names, nil
	}))

	must(w.Bind("saveDomainList", func(name string, domains []string) error {
		if !profileNameIsValid(name) {
			return fmt.Errorf("domain list name %q is not valid", name)
		}
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.SaveDomainList", DomainListSaveRequest{Name: name, Domains: domains}, &struct{}{})
	}))

	must(w.Bind("loadDomainList", func(name string) ([]string, error) {
		client, err := ipcDial()
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var domains []string
		if err := client.Call("Bridge.LoadDomainList", name, &domains); err != nil {
			return nil, err
		}
		return domains, nil
	}))

	must(w.Bind("deleteDomainList", func(name string) error {
		client, err := ipcDial()
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Call("Bridge.DeleteDomainList", name, &struct{}{})
	}))

	must(w.Bind("importConfig", func() (*ImportedConfig, error) {
		path, err := zenityFilePicker("Import configuration")
		if err != nil || path == "" {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(path)
		suggested := strings.TrimSuffix(base, filepath.Ext(base))
		return &ImportedConfig{SuggestedName: suggested, Text: string(data)}, nil
	}))
}

// zenityFilePicker shells out to zenity (present on GNOME/Cinnamon/most
// desktop Linux by default) for a native file-open dialog — webview_go
// exposes no cross-toolkit file chooser of its own, and driving GTK's
// FileChooserDialog directly from Go would mean hand-writing cgo bindings
// for it specifically. Returns "", nil (not an error) if the user cancels.
func zenityFilePicker(title string) (string, error) {
	out, err := exec.Command("zenity", "--file-selection", "--title="+title).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil // user cancelled
		}
		return "", fmt.Errorf("zenity not available or failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func imageToDataURI(path string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	mime := "image/png"
	switch ext {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".gif":
		mime = "image/gif"
	case ".webp":
		mime = "image/webp"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
