#!/bin/bash
# Builds spectrune's .deb package. Run from the repo root:
#   ./linux-build/build-deb.sh
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=$(grep -oP 'const appVersion = "\K[^"]+' version.go)
PKGROOT="/tmp/spectrune-deb-build/spectrune_${VERSION}_amd64"

echo "Building spectrune ${VERSION} for Linux..."
PKG_CONFIG_PATH="$(pwd)/linux-build/pkgconfig-shim" GOOS=linux GOARCH=amd64 \
    go build -o /tmp/spectrune-deb-build/spectrune .

rm -rf "$PKGROOT"
mkdir -p "$PKGROOT/DEBIAN" \
    "$PKGROOT/usr/bin" \
    "$PKGROOT/usr/share/applications" \
    "$PKGROOT/usr/share/icons/hicolor/256x256/apps" \
    "$PKGROOT/lib/systemd/system"

sed "s/^Version: .*/Version: ${VERSION}/" linux-build/DEBIAN/control > "$PKGROOT/DEBIAN/control"
install -m 755 linux-build/DEBIAN/postinst "$PKGROOT/DEBIAN/postinst"
install -m 755 linux-build/DEBIAN/prerm "$PKGROOT/DEBIAN/prerm"
install -m 755 linux-build/DEBIAN/postrm "$PKGROOT/DEBIAN/postrm"

install -m 755 /tmp/spectrune-deb-build/spectrune "$PKGROOT/usr/bin/spectrune"
install -m 644 linux-build/spectrune.desktop "$PKGROOT/usr/share/applications/spectrune.desktop"
install -m 644 linux-build/assets/spectrune.png "$PKGROOT/usr/share/icons/hicolor/256x256/apps/spectrune.png"
install -m 644 linux-build/spectrune.service "$PKGROOT/lib/systemd/system/spectrune.service"

fakeroot dpkg-deb --build --root-owner-group "$PKGROOT" \
    "/tmp/spectrune-deb-build/spectrune_${VERSION}_amd64.deb"

echo "Built: /tmp/spectrune-deb-build/spectrune_${VERSION}_amd64.deb"
