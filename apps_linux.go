// App enumeration and per-app launch for Linux — the direct analog of
// installedapps.go's Windows Start-Menu scan, but simpler: freedesktop.org
// .desktop files are already a structured, standard listing of installed
// GUI apps, no registry/Squirrel-path archaeology needed.
package main

import (
	"bufio"
	"fmt"
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
// environment: nsenter only isolates networking, so once privileges are
// dropped inside the namespace the launched app needs these to display on
// the real desktop, use the sound server, talk to D-Bus, etc.
type LaunchAppRequest struct {
	DesktopID string
	Env       map[string]string // DISPLAY, WAYLAND_DISPLAY, XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS
}

// LaunchApp runs the given .desktop entry's command inside the tunnel's
// namespace, dropped to the calling user's own UID/GID first (this method
// runs in the root daemon, which is the only thing with permission to
// enter the namespace at all — see the plan's "Per-app launch mechanism").
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

	args := []string{"--net=/var/run/netns/" + netnsName,
		"setpriv", "--reuid=" + uidStr, "--regid=" + gidStr, "--clear-groups", "--inh-caps=-all", "--",
		"env"}
	for _, k := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if v, ok := req.Env[k]; ok && v != "" {
			args = append(args, k+"="+v)
		}
	}
	execArgs := stripFieldCodes(target.Exec)
	extraFlags, err := multiInstanceFlags(execArgs, uidStr, gidStr)
	if err != nil {
		return fmt.Errorf("multiInstanceFlags: %w", err)
	}
	execArgs = append(execArgs, extraFlags...)
	args = append(args, execArgs...)

	cmd := exec.Command("nsenter", args...)
	// Detached, not waited on — this is "open an app," not "run a command
	// and collect its output." The launched app keeps running after this
	// RPC returns, same as double-clicking it normally would.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nsenter: %w", err)
	}
	go cmd.Wait() // reap it, avoid a zombie; nothing else needs its exit status
	return nil
}

// multiInstanceHandlers maps a launcher binary's basename to whatever it
// takes to start a genuinely separate instance instead of forwarding to
// (and exiting in favor of) an already-running one — most single-
// instance-locked apps support this for exactly this reason (running
// multiple accounts side by side), it's just off by default. Windows
// never needed this: it intercepted an already-running process's live
// traffic instead of launching a fresh one, so single-instance locking
// never came up there.
//
// Confirmed necessary live 2026-09-08 for two different apps with two
// different failure shapes: Telegram without -many just handed off to
// the already-running instance and the new (correctly namespaced)
// process exited immediately, so nothing was tunneled at all. Firefox
// without -new-instance did something more deceptive — it looked like
// nothing failed (Firefox itself didn't error, no process even briefly
// appeared and vanished), but ps showed the "new" tab was actually a
// -contentproc child of the pre-existing, non-namespaced Firefox (PID
// unrelated to the one nsenter started) — i.e. it silently forwarded the
// open-URL request to the old instance over Firefox's remoting protocol
// rather than rendering anything in the process nsenter actually placed
// in the tunnel's namespace. -new-instance alone still refuses to start
// (profile lock held by the already-running instance) — needs a distinct
// -profile directory too, created here and chowned to the caller so the
// dropped-privilege process can actually write to it.
var multiInstanceHandlers = map[string]func(uid, gid string) ([]string, error){
	"Telegram": func(uid, gid string) ([]string, error) {
		return []string{"-many"}, nil
	},
	"firefox": func(uid, gid string) ([]string, error) {
		dir, err := freshOwnedTempDir("spectrune-firefox-profile-", uid, gid)
		if err != nil {
			return nil, err
		}
		return []string{"-new-instance", "-profile", dir}, nil
	},
}

func freshOwnedTempDir(prefix, uid, gid string) (string, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	uidNum, err := strconv.Atoi(uid)
	if err != nil {
		return "", fmt.Errorf("bad uid %q: %w", uid, err)
	}
	gidNum, err := strconv.Atoi(gid)
	if err != nil {
		return "", fmt.Errorf("bad gid %q: %w", gid, err)
	}
	if err := os.Chown(dir, uidNum, gidNum); err != nil {
		return "", err
	}
	return dir, nil
}

func multiInstanceFlags(execArgs []string, uid, gid string) ([]string, error) {
	if len(execArgs) == 0 {
		return nil, nil
	}
	base := filepath.Base(execArgs[0])
	handler, ok := multiInstanceHandlers[base]
	if !ok {
		return nil, nil
	}
	return handler(uid, gid)
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
