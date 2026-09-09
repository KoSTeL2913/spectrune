# Building Spectrune for Linux

```
PKG_CONFIG_PATH="$(pwd)/linux-build/pkgconfig-shim" GOOS=linux GOARCH=amd64 go build -o spectrune-linux ./cmd/spectrune
```

Or build the full `.deb` (binary + systemd unit + desktop entry + icon):

```
./linux-build/build-deb.sh
```

## Why the pkg-config shim

`github.com/webview/webview_go`'s cgo directives hardcode the pkg-config
package name `webkit2gtk-4.0`, which no longer exists on Ubuntu 24.04+ /
other recent distros — only `webkit2gtk-4.1` is packaged now (a rename,
not a real API break for the parts this app uses; `webkit2gtk-6.0` is the
actual GTK4 rewrite, unrelated). `pkgconfig-shim/webkit2gtk-4.0.pc` and
`javascriptcoregtk-4.0.pc` just point `Requires:`/`Libs:` at the real 4.1
packages under the old name.

Runtime (not just build-time) needs `libwebkit2gtk-4.1-0`, `libgtk-3-0`,
`iproute2`, `util-linux`, `iptables`, and `ipset` — already declared as
`.deb` dependencies (`linux-build/DEBIAN/control`); a tarball install
needs them installed separately.

## Running

Needs root for the daemon (creates the routing cgroup, TUN device, and
iptables/ipset rules):

```
sudo systemctl enable --now spectrune.service   # normal install, via the .deb's postinst
spectrune /gui                                   # the GUI, as your normal user
```

For manual/dev runs without the systemd unit:

```
sudo ./spectrune-linux /service &
./spectrune-linux /gui
```

First run: the daemon creates a `spectrune` system group and adds
`$SUDO_USER` to it (falls back to a printed instruction if not run via
sudo). **You need a fresh login shell/session for that group membership
to take effect** — `sg spectrune -c '...'` works around this without a
full re-login.
