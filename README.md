# Spectrune

A standalone VPN client for AmneziaWG/WireGuard with **per-application traffic
routing** — pick which apps (or which domains) go through the tunnel and
leave the rest of the system untouched, instead of an all-or-nothing toggle.
Windows and Linux, one shared GUI.

## Download

| | |
|---|---|
| **Windows** | [spectrune.msi](https://github.com/KoSTeL2913/spectrune/releases/latest/download/spectrune.msi) |
| **Linux** | [.deb (Debian/Ubuntu) and .tar.gz (any distro)](https://github.com/KoSTeL2913/spectrune/releases/latest) |

See a release's own notes for the exact filenames of that version.

## What it does

- Per-application routing: select an app in the GUI and its traffic — even
  an already-running instance, not just freshly-launched ones — goes
  through the tunnel while everything else on the machine doesn't.
- Named, toggleable domain lists (Linux): route specific domains through
  the tunnel regardless of which process accesses them.
- Multiple saved profiles, app presets, hotkeys, theming, self-update.

## Platform notes

The two platforms use genuinely different mechanisms, not just different
glue code:

- **Windows**: a WFP (Windows Filtering Platform)-based per-process
  interception — an already-running process can be matched by PID and
  redirected into the tunnel live.
- **Linux**: a cgroup + fwmark + policy-routing setup (`ip rule`/`iptables`/
  `ipset`) — matched processes get moved into a dedicated cgroup, and a
  domain-list match marks packets by destination IP instead, so both
  mechanisms share one tunnel.

## Repo layout

```
cmd/spectrune/     the whole application — one Go package, split into
                    OS-specific files by filename suffix (_windows.go /
                    _linux.go); files with neither suffix are shared
                    (profile format, the GUI's HTML/CSS/JS)
internal/netstack/  vendored userspace TUN/IP stack support
installer/          the Windows MSI (WiX source + build output)
linux-build/        the .deb packaging (control files, systemd unit,
                    desktop entry) and the pkg-config shim needed to
                    build against webkit2gtk on newer distros
```

## Building

Windows (from Linux, cross-compiling with mingw — see `installer/spectrune.wxs`
for the full MSI packaging steps):

```
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
    go build -ldflags="-H=windowsgui" -o spectrune.exe ./cmd/spectrune
```

Linux — see `linux-build/README.md` for the pkg-config shim this needs and
the full `.deb` build:

```
PKG_CONFIG_PATH="$(pwd)/linux-build/pkgconfig-shim" GOOS=linux GOARCH=amd64 \
    go build -o spectrune-linux ./cmd/spectrune
```
