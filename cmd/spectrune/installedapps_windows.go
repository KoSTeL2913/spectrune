// Ported verbatim from amneziawg-windows-client/ui/installedapps.go (fixed
// there 2026-09-02 for Squirrel/Electron-style installers like Discord)
// — pure registry enumeration, no manager/l18n dependency, so it copies
// straight across.
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/sys/windows/registry"
)

type installedApp struct {
	Name string
	Path string
}

// normalizeAppName strips everything but letters/digits and lowercases, so
// "GitHub Desktop" and "GitHubDesktop.exe" compare equal — the standard
// Squirrel/electron-builder convention of naming the main exe after the
// product name with spaces removed.
func normalizeAppName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// resolveSquirrelExe finds an app's main executable under its InstallLocation
// when the Uninstall registry entry has no usable DisplayIcon. Many
// Squirrel/electron-builder installers (Discord, GitHub Desktop, ...) either
// leave DisplayIcon empty or point it at a non-.exe icon file, and never
// register an App Paths entry — but always set InstallLocation, with the
// real exe either directly inside it or inside a versioned "app-X.Y.Z"
// subdirectory (the Squirrel convention).
func resolveSquirrelExe(installLocation, displayName string) string {
	want := normalizeAppName(displayName)
	if want == "" {
		return ""
	}
	searchDirs := []string{installLocation}
	if appDirs, err := filepath.Glob(filepath.Join(installLocation, "app-*")); err == nil {
		for i := len(appDirs) - 1; i >= 0; i-- {
			searchDirs = append(searchDirs, appDirs[i])
		}
	}
	for _, dir := range searchDirs {
		exes, err := filepath.Glob(filepath.Join(dir, "*.exe"))
		if err != nil {
			continue
		}
		for _, exe := range exes {
			if normalizeAppName(strings.TrimSuffix(filepath.Base(exe), ".exe")) == want {
				return exe
			}
		}
	}
	return ""
}

// isVersionSegment reports whether s looks like a version-number path
// component (e.g. "152.0.4191.53") rather than a real directory name.
func isVersionSegment(s string) bool {
	if s == "" || !strings.Contains(s, ".") {
		return false
	}
	for _, r := range s {
		if r != '.' && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// preferStableExe rewrites a path that lives directly inside a
// version-numbered subfolder to the same-named exe one directory up, if
// that stable launcher exists on disk. Chromium-based installs (Edge is
// the confirmed case, 2026-09-03: registry DisplayIcon pointed at
// ...\Application\152.0.4191.53\msedge.exe, but the process that's
// actually running — and the one Spectrune's per-app matching sees via
// GetExtendedTcpTable — is the version-independent
// ...\Application\msedge.exe launcher stub one level up) register their
// Uninstall entry's DisplayIcon against the current version's real binary
// instead of the stable stub, so IncludedApps saved from the picker never
// matched live traffic without this normalization.
func preferStableExe(path string) string {
	dir := filepath.Dir(path)
	if !isVersionSegment(filepath.Base(dir)) {
		return path
	}
	candidate := filepath.Join(filepath.Dir(dir), filepath.Base(path))
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return path
}

// enumerateInstalledApps discovers installed applications purely from the
// registry (App Paths + Uninstall entries) — no COM/.lnk shortcut parsing,
// to avoid pulling in a new dependency for that.
func enumerateInstalledApps() []installedApp {
	seen := make(map[string]bool)
	var apps []installedApp

	add := func(name, path string) {
		path = strings.Trim(path, `"`)
		path = preferStableExe(path)
		lower := strings.ToLower(path)
		if !strings.HasSuffix(lower, ".exe") {
			return
		}
		if seen[lower] {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		seen[lower] = true
		if name == "" {
			name = path
		}
		apps = append(apps, installedApp{Name: name, Path: path})
	}

	appPathsRoots := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths`},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\App Paths`},
	}
	for _, root := range appPathsRoots {
		k, err := registry.OpenKey(root.root, root.path, registry.READ)
		if err != nil {
			continue
		}
		names, err := k.ReadSubKeyNames(-1)
		if err == nil {
			for _, subName := range names {
				sk, err := registry.OpenKey(root.root, root.path+`\`+subName, registry.READ)
				if err != nil {
					continue
				}
				path, _, err := sk.GetStringValue("")
				sk.Close()
				if err == nil && path != "" {
					add(strings.TrimSuffix(subName, ".exe"), path)
				}
			}
		}
		k.Close()
	}

	uninstallRoots := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
	}
	for _, root := range uninstallRoots {
		k, err := registry.OpenKey(root.root, root.path, registry.READ)
		if err != nil {
			continue
		}
		names, err := k.ReadSubKeyNames(-1)
		if err == nil {
			for _, subName := range names {
				sk, err := registry.OpenKey(root.root, root.path+`\`+subName, registry.READ)
				if err != nil {
					continue
				}
				if systemComponent, _, err := sk.GetIntegerValue("SystemComponent"); err == nil && systemComponent == 1 {
					sk.Close()
					continue
				}
				displayName, _, _ := sk.GetStringValue("DisplayName")
				displayIcon, _, _ := sk.GetStringValue("DisplayIcon")
				installLocation, _, _ := sk.GetStringValue("InstallLocation")
				sk.Close()

				// DisplayIcon is often "C:\path\to\app.exe,0" — strip the icon index.
				path := displayIcon
				if idx := strings.LastIndex(path, ","); idx > 0 {
					if _, err := strconv.Atoi(strings.TrimSpace(path[idx+1:])); err == nil {
						path = path[:idx]
					}
				}
				if !strings.HasSuffix(strings.ToLower(path), ".exe") && installLocation != "" {
					// DisplayIcon missing or not an exe (empty, or an .ico as
					// with GitHub Desktop) — fall back to InstallLocation.
					path = resolveSquirrelExe(installLocation, displayName)
				}
				if path == "" {
					continue
				}
				add(displayName, path)
			}
		}
		k.Close()
	}

	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	return apps
}
