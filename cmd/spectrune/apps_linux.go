// App enumeration and per-app launch for Linux — the direct analog of
// installedapps.go's Windows Start-Menu scan, but simpler: freedesktop.org
// .desktop files are already a structured, standard listing of installed
// GUI apps, no registry/Squirrel-path archaeology needed.
package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// DesktopApp is what the GUI's app picker lists — id is the .desktop
// filename stem (e.g. "firefox" from "firefox.desktop"), stable enough to
// store in a profile's IncludedApps.
type DesktopApp struct {
	ID   string
	Name string
	Exec string
	Icon string
}

// desktopDirs lists every directory to scan for .desktop files. This runs
// inside the root daemon (ListApps is an RPC), where os.UserHomeDir()
// resolves to /root, not the real desktop user's home — so a per-user
// entry that only exists under ~/.local/share/applications (confirmed
// live 2026-09-08: Telegram's own self-registered .desktop file lives
// exactly there, not in any system-wide directory) was invisible even
// though the app itself was installed and running. Globbing /home/*
// instead of relying on the daemon's own notion of "home" sidesteps that
// without needing the caller to pass its identity over the RPC — a
// reasonable trade for a personal-desktop app; would need revisiting for
// genuine multi-user support.
func desktopDirs() []string {
	dirs := []string{"/usr/share/applications", "/usr/local/share/applications"}
	if homes, err := filepath.Glob("/home/*/.local/share/applications"); err == nil {
		dirs = append(dirs, homes...)
	}
	return dirs
}

// listDesktopApps scans the standard freedesktop.org application
// directories, skipping entries marked NoDisplay/Hidden (the same ones a
// normal desktop's app launcher would hide).
func listDesktopApps() ([]DesktopApp, error) {
	seen := make(map[string]bool)
	var apps []DesktopApp
	for _, dir := range desktopDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // dir may not exist — not an error
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".desktop") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".desktop")
			if seen[id] {
				continue // an earlier dir in the list wins, matches XDG precedence
			}
			app, ok, err := parseDesktopFile(filepath.Join(dir, e.Name()), id)
			if err != nil || !ok {
				continue
			}
			seen[id] = true
			apps = append(apps, app)
		}
	}
	return apps, nil
}

func parseDesktopFile(path, id string) (DesktopApp, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return DesktopApp{}, false, err
	}
	defer f.Close()

	app := DesktopApp{ID: id}
	inEntry := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[Desktop Entry]" {
			inEntry = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = false
			continue
		}
		if !inEntry {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]
		switch key {
		case "Name":
			if app.Name == "" {
				app.Name = val
			}
		case "Exec":
			app.Exec = val
		case "Icon":
			app.Icon = val
		case "NoDisplay", "Hidden":
			if strings.EqualFold(val, "true") {
				return DesktopApp{}, false, nil
			}
		case "Type":
			if val != "Application" {
				return DesktopApp{}, false, nil
			}
		}
	}
	if app.Name == "" || app.Exec == "" {
		return DesktopApp{}, false, nil
	}
	return app, true, nil
}

// tokenizeExec splits a .desktop Exec= value the way the spec requires —
// quote-aware, not a naive strings.Fields split. Confirmed necessary live
// 2026-09-08: Telegram's own self-registered .desktop file quotes its
// path in single quotes specifically because it contains a space
// ("Рабочий стол"), e.g. Exec='/home/kostel/Рабочий стол/Telegram' -- %U
// — plain whitespace-splitting would tear that one path into two bogus
// tokens ("'/home/kostel/Рабочий" and "стол/Telegram'").
func tokenizeExec(execLine string) []string {
	var tokens []string
	var cur strings.Builder
	var quote rune // 0, '\'', or '"'
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range execLine {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// stripFieldCodes tokenizes execLine and drops .desktop Exec's
// %f/%F/%u/%U/%i/%c/%k field codes (freedesktop.org spec) — this launcher
// never has a file/URL to hand the app, so they're just dropped rather
// than substituted.
func stripFieldCodes(execLine string) []string {
	fields := tokenizeExec(execLine)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		switch f {
		case "%f", "%F", "%u", "%U", "%i", "%c", "%k":
			continue
		}
		out = append(out, f)
	}
	return out
}

// LaunchAppRequest bundles LaunchApp's arguments — net/rpc methods take
// exactly one request value. The caller (the GUI, already running as the
// target user in the target desktop session) supplies its own session
// environment: the launched process needs it to display on the real
// desktop, use the sound server, talk to D-Bus, etc.
type LaunchAppRequest struct {
	DesktopID string
	Env       map[string]string // DISPLAY, WAYLAND_DISPLAY, XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS
}

// execTargetBase returns the .desktop entry's command basename (e.g.
// "firefox" from "firefox %u") — used both to launch it and, via
// resolveAppBinaries/matchingPIDs below, to recognize it if it's already
// running.
func execTargetBase(execLine string) string {
	args := stripFieldCodes(execLine)
	if len(args) == 0 {
		return ""
	}
	return filepath.Base(args[0])
}

// LaunchApp starts the given .desktop entry's command as the calling
// user (no privilege drop needed here — the daemon just runs it exactly
// like a normal launcher would) and immediately moves the resulting PID
// into the routing cgroup (routing_linux.go's addPIDToCgroup) so it's
// tunneled from its very first packet, rather than waiting for the next
// matchLoop tick. Unlike the old namespace-based version, this is *not*
// trying to force a separate instance — if the app has single-instance
// locking and hands off to an already-running one, that's fine now: the
// whole point of switching to cgroups was to be able to include an
// already-running process directly, so there's nothing to work around.
func launchApp(req LaunchAppRequest) error {
	var target DesktopApp
	found := false
	apps, err := listDesktopApps()
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.ID == req.DesktopID {
			target = a
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no such app %q", req.DesktopID)
	}

	uidStr := req.Env["_CALLER_UID"]
	gidStr := req.Env["_CALLER_GID"]
	if uidStr == "" || gidStr == "" {
		u, err := callerFallbackUser()
		if err != nil {
			return fmt.Errorf("determining caller uid/gid: %w", err)
		}
		uidStr, gidStr = u.Uid, u.Gid
	}

	args := []string{"--reuid=" + uidStr, "--regid=" + gidStr, "--clear-groups", "--inh-caps=-all", "--", "env"}
	for _, k := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if v, ok := req.Env[k]; ok && v != "" {
			args = append(args, k+"="+v)
		}
	}
	args = append(args, stripFieldCodes(target.Exec)...)

	cmd := exec.Command("setpriv", args...)
	// Detached, not waited on — this is "open an app," not "run a command
	// and collect its output." The launched app keeps running after this
	// RPC returns, same as double-clicking it normally would.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("setpriv: %w", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait() // reap it, avoid a zombie; nothing else needs its exit status

	// Best-effort — if this fails (e.g. the process already exited, or
	// itself immediately re-exec'd into something with a different PID,
	// which setpriv's own exec doesn't do), matchLoop will pick it up on
	// its own within matchInterval anyway once it (or whatever it forked)
	// shows up under its real name.
	if err := addPIDToCgroup(pid); err != nil {
		log.Printf("launchApp: addPIDToCgroup(%d): %v (matchLoop will retry)", pid, err)
	}
	return nil
}

// resolveAppBinaries turns a profile's IncludedApps (.desktop entry IDs)
// into the command basenames matchingPIDs actually compares against.
func resolveAppBinaries(desktopIDs []string) []string {
	if len(desktopIDs) == 0 {
		return nil
	}
	apps, err := listDesktopApps()
	if err != nil {
		return nil
	}
	byID := make(map[string]DesktopApp, len(apps))
	for _, a := range apps {
		byID[a.ID] = a
	}
	var out []string
	for _, id := range desktopIDs {
		if a, ok := byID[id]; ok {
			if base := execTargetBase(a.Exec); base != "" {
				out = append(out, base)
			}
		}
	}
	return out
}

// matchingPIDs scans /proc for running processes whose real executable
// matches one of targets (command basenames from resolveAppBinaries).
// Matching is by basename, not full path — most apps run through a
// wrapper script (/usr/bin/firefox is a shell script; the process /proc/
// <pid>/exe actually points to is /usr/lib/firefox/firefox-bin) so exact-
// path matching would never hit. "firefox-bin" starting with "firefox-"
// (target + "-") catches that pattern, which is common enough (Chrome,
// Firefox, most Electron apps) to be worth a dedicated check rather than
// requiring an exact basename match.
//
// Case-insensitive: confirmed live 2026-09-09 that Discord's .desktop
// Exec= is the lowercase wrapper script "/usr/bin/discord", but the real
// Electron binary it execs into is "/home/.../app-<ver>/Discord" (capital
// D) — an exact-case comparison never matched, so Discord was silently
// never added to the tunnel's cgroup despite being "included."
func matchingPIDs(targets []string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	lowerTargets := make([]string, len(targets))
	for i, t := range targets {
		lowerTargets[i] = strings.ToLower(t)
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue // permission denied (not ours/not readable) or already gone
		}
		base := strings.ToLower(filepath.Base(exe))
		for _, t := range lowerTargets {
			if base == t || strings.HasPrefix(base, t+"-") {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

// callerFallbackUser is used only if the RPC caller didn't supply
// uid/gid explicitly — falls back to whichever non-root user owns the
// current graphical session, best-effort.
func callerFallbackUser() (*user.User, error) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		return user.Lookup(sudoUser)
	}
	return nil, fmt.Errorf("no caller uid/gid supplied and SUDO_USER not set")
}
