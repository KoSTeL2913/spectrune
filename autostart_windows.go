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

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	// Quoted, no arguments — same as a normal double-click launch
	// (runLegacyDirect's no-args path), so autostart behaves exactly like
	// the user starting it themselves.
	if err := k.SetStringValue(autostartValueName, `"`+exePath+`"`); err != nil {
		return fmt.Errorf("setting Run value: %w", err)
	}
	return nil
}
