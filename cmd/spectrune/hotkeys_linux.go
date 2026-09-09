// Global per-profile hotkeys — the Linux analog of tray_windows.go's
// RegisterHotKey-based refreshHotkeys/parseHotkeyString/vkFromKeyName.
// Uses XGrabKey (via xgbutil/keybind, which handles the fiddly NumLock/
// CapsLock modifier-variant grabbing for us) on the root window instead
// of Win32's RegisterHotKey — same effect: the binding fires regardless
// of which window has focus. The hotkey *capture* UI needs no Linux-
// specific code at all: webui_html.go's capture widget already builds
// the "Ctrl+Alt+F1" string from real DOM keydown events in JS, so this
// file only needs to parse that same string and act on it.
package main

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/keybind"
	"github.com/BurntSushi/xgbutil/xevent"
)

// rpcCaller is the subset of *rpc.Client's method set listProfilesRPC
// needs — just enough to keep that helper's signature readable without
// importing net/rpc here solely for the type name.
type rpcCaller interface {
	Call(serviceMethod string, args any, reply any) error
}

// hotkeyRefreshInterval mirrors the poll cadence tray_windows.go's
// pollTrayState already uses for its own state/hotkey refresh — no push
// notification path exists from the daemon for "a profile's Hotkey field
// changed," so periodic re-reading is what Windows does too.
const hotkeyRefreshInterval = 3 * time.Second

type hotkeyManager struct {
	xu   *xgbutil.XUtil
	root xproto.Window
	// profile name -> the exact "Ctrl+Alt+F1" string currently grabbed for
	// it, so a refresh only touches what actually changed.
	bound map[string]string
}

func startHotkeyManager() {
	xu, err := xgbutil.NewConn()
	if err != nil {
		log.Printf("hotkeys: xgbutil.NewConn: %v (global hotkeys unavailable — is this an X11 session?)", err)
		return
	}
	keybind.Initialize(xu)
	hm := &hotkeyManager{xu: xu, root: xu.RootWin(), bound: map[string]string{}}

	go xevent.Main(xu)

	go func() {
		for {
			hm.refresh()
			time.Sleep(hotkeyRefreshInterval)
		}
	}()
}

func (hm *hotkeyManager) refresh() {
	client, err := ipcDial()
	if err != nil {
		return // daemon not up yet — try again next tick
	}
	defer client.Close()

	names, err := listProfilesRPC(client)
	if err != nil {
		return
	}

	current := make(map[string]string, len(names))
	for _, name := range names {
		var cfg LinuxConfig
		if err := client.Call("Bridge.LoadProfile", name, &cfg); err != nil {
			continue
		}
		if cfg.Hotkey != "" {
			current[name] = cfg.Hotkey
		}
	}

	// Unbind anything removed or changed.
	for name, oldHK := range hm.bound {
		if current[name] != oldHK {
			hm.ungrab(oldHK)
			delete(hm.bound, name)
		}
	}
	// Bind anything new or changed.
	for name, hk := range current {
		if hm.bound[name] == hk {
			continue
		}
		xStr, ok := toXGBKeyString(hk)
		if !ok {
			log.Printf("hotkeys: profile %q has unparseable hotkey %q", name, hk)
			continue
		}
		profileName := name // capture for the closure
		cb := keybind.KeyPressFun(func(xu *xgbutil.XUtil, e xevent.KeyPressEvent) {
			go quickToggleProfile(profileName)
		})
		if err := cb.Connect(hm.xu, hm.root, xStr, true); err != nil {
			log.Printf("hotkeys: binding %q for profile %q failed: %v (probably already bound by another app)", hk, name, err)
			continue
		}
		hm.bound[name] = hk
	}
}

func (hm *hotkeyManager) ungrab(hk string) {
	xStr, ok := toXGBKeyString(hk)
	if !ok {
		return
	}
	mods, keycodes, err := keybind.ParseString(hm.xu, xStr)
	if err != nil {
		return
	}
	for _, kc := range keycodes {
		keybind.Ungrab(hm.xu, hm.root, mods, kc)
	}
}

// toXGBKeyString translates the JS capture widget's "Ctrl+Alt+F1" form
// into xgbutil/keybind.ParseString's expected "control-mod1-F1" form —
// same modifier names X11 itself uses (Alt is conventionally Mod1 on
// almost every X11 setup, including this one), different spelling and
// separator.
func toXGBKeyString(s string) (string, bool) {
	parts := strings.Split(s, "+")
	var out []string
	haveKey := false
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch strings.ToLower(p) {
		case "ctrl", "control":
			out = append(out, "control")
		case "alt":
			out = append(out, "mod1")
		case "shift":
			out = append(out, "shift")
		default:
			if haveKey {
				return "", false // more than one non-modifier part
			}
			key, ok := normalizeKeyName(p)
			if !ok {
				return "", false
			}
			out = append(out, key)
			haveKey = true
		}
	}
	if !haveKey || len(out) < 2 {
		return "", false
	}
	return strings.Join(out, "-"), true
}

// normalizeKeyName covers exactly what the JS capture widget can produce
// (webui_html.go): A-Z, 0-9, F1-F12 — mapped to the X11 keysym names
// xgbutil's StrToKeycodes/keysyms table expects.
func normalizeKeyName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	switch {
	case len(name) == 1 && name[0] >= 'A' && name[0] <= 'Z':
		return name, true
	case len(name) == 1 && name[0] >= '0' && name[0] <= '9':
		return name, true
	case len(name) >= 2 && len(name) <= 3 && (name[0] == 'F' || name[0] == 'f'):
		n, err := strconv.Atoi(name[1:])
		if err == nil && n >= 1 && n <= 12 {
			return "F" + strconv.Itoa(n), true
		}
	}
	return "", false
}

// listProfilesRPC is a tiny helper so refresh() reads as one RPC-call
// style throughout.
func listProfilesRPC(client rpcCaller) ([]string, error) {
	var names []string
	err := client.Call("Bridge.ListProfiles", struct{}{}, &names)
	return names, err
}

// quickToggleProfile is a hotkey's action: Disconnect if name is already
// the active profile, otherwise switch to it — same semantics as
// tray_windows.go's quickToggleProfile.
func quickToggleProfile(name string) {
	client, err := ipcDial()
	if err != nil {
		return
	}
	defer client.Close()

	var state StateReply
	if err := client.Call("Bridge.State", struct{}{}, &state); err != nil {
		log.Printf("quickToggleProfile(%q): State: %v", name, err)
		return
	}
	if state.Connected && state.ProfileName == name {
		if err := client.Call("Bridge.Disconnect", struct{}{}, &struct{}{}); err != nil {
			log.Printf("quickToggleProfile(%q): Disconnect: %v", name, err)
		}
		return
	}
	if state.Connected {
		if err := client.Call("Bridge.Disconnect", struct{}{}, &struct{}{}); err != nil {
			log.Printf("quickToggleProfile(%q): Disconnect (switching): %v", name, err)
			return
		}
	}
	if err := client.Call("Bridge.Connect", name, &struct{}{}); err != nil {
		log.Printf("quickToggleProfile(%q): Connect: %v", name, err)
	}
}
