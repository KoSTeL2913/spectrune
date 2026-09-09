#!/bin/bash
# Installs Spectrune from this tarball's extracted contents. Run as root
# (sudo ./install.sh) from the directory this script lives in.
set -euo pipefail
cd "$(dirname "$0")"

if [ "$(id -u)" -ne 0 ]; then
    echo "Run this as root: sudo ./install.sh" >&2
    exit 1
fi

install -m 755 spectrune /usr/bin/spectrune
install -m 644 spectrune.service /lib/systemd/system/spectrune.service
install -m 644 spectrune.desktop /usr/share/applications/spectrune.desktop
install -Dm 644 assets/spectrune.png /usr/share/icons/hicolor/256x256/apps/spectrune.png

getent group spectrune >/dev/null || groupadd -r spectrune

if [ -n "${SUDO_USER:-}" ]; then
    usermod -aG spectrune "$SUDO_USER" || true
    echo "Added $SUDO_USER to the 'spectrune' group — log out and back in for it to take effect."
else
    echo "Run 'sudo usermod -aG spectrune <your-username>' and log back in to use Spectrune without a password prompt each time."
fi

systemctl daemon-reload
systemctl enable --now spectrune.service
update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
gtk-update-icon-cache /usr/share/icons/hicolor >/dev/null 2>&1 || true

echo "Spectrune installed. Find it in your application menu, or run: spectrune /gui"
