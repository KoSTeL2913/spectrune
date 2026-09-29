// "Launch on Windows startup" toggle (webui_html.go's settings panel) —
// a plain per-user Run key, not anything service-related: the daemon
// (service_windows.go) already starts on its own via the SCM's own
// "Automatic" start type, so this is only about the GUI/tray front-end
// reappearing after login without the user launching it by hand. Written
// under HKCU, not HKLM, so it needs no elevation — the GUI already runs
// as the plain logged-in user, same reasoning installedapps_windows.go's
// registry reads don't need elevation either.
package main

import (
	"fmt"
	"log"
	"os"

	"golang.org/x/sys/windows/registry"
)

const autostartRunKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const autostartValueName = "Spectrune"

// isAutostartEnabled reports whether the Run value exists at all —
// deliberately not checking its content matches the current exe path.
// The path could only go stale if Spectrune were reinstalled to a
// different location, which doesn't happen in practice (same install
// path every release); treating "value exists" as "enabled" keeps this
// simple and avoids the toggle ever flickering to "off" on its own.
func isAutostartEnabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	_, _, err = k.GetStringValue(autostartValueName)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func setAutostartEnabled(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening Run key: %w", err)
	}
	defer k.Close()

	if !enable {
		err := k.DeleteValue(autostartValueName)
		if err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("deleting Run value: %w", err)
		}
		return nil
	}

	cmd, err := autostartCommand()
	if err != nil {
		return err
	}
	if err := k.SetStringValue(autostartValueName, cmd); err != nil {
		return fmt.Errorf("setting Run value: %w", err)
	}
	return nil
}

// autostartCommand is the Run value: the exe with /gui, exactly what the
// desktop/Start Menu shortcuts run (installer/spectrune.wxs). Before
// v2.0.41.0 it was the bare exe path with no arguments, which doesn't
// open the GUI at all — no args is runLegacyDirect, the old AmneziaWG
// companion mode — so autostart silently did nothing on every Windows
// version. Reported 2026-09-29 on Windows 11.
func autostartCommand() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	return `"` + exePath + `" /gui`, nil
}

// repairAutostartEntry rewrites an existing Run value that doesn't match
// autostartCommand — i.e. one written by a pre-v2.0.41.0 build without
// /gui — so users who already turned the toggle on get a working
// autostart after updating, without having to toggle it off and on. A
// missing value (toggle off) is left alone.
func repairAutostartEntry() {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	current, _, err := k.GetStringValue(autostartValueName)
	if err != nil {
		return
	}
	want, err := autostartCommand()
	if err != nil || current == want {
		return
	}
	if err := k.SetStringValue(autostartValueName, want); err != nil {
		log.Printf("repairing autostart Run value: %v", err)
		return
	}
	log.Printf("repaired autostart Run value: %q -> %q", current, want)
}
