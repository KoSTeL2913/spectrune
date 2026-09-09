#!/bin/bash
# Removes what install.sh set up. Run as root (sudo ./uninstall.sh).
# Pass --purge to also delete saved profiles/state under /var/lib/spectrune
# and remove the 'spectrune' group.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "Run this as root: sudo ./uninstall.sh [--purge]" >&2
    exit 1
fi

systemctl stop spectrune.service 2>/dev/null || true
systemctl disable spectrune.service 2>/dev/null || true

rm -f /usr/bin/spectrune
rm -f /lib/systemd/system/spectrune.service
rm -f /usr/share/applications/spectrune.desktop
rm -f /usr/share/icons/hicolor/256x256/apps/spectrune.png
systemctl daemon-reload

if [ "${1:-}" = "--purge" ]; then
    rm -rf /var/lib/spectrune
    iptables -t mangle -D OUTPUT -m cgroup --path spectrune -j MARK --set-mark 0xca11 2>/dev/null || true
    iptables -t nat -D POSTROUTING -o spectrune0 -j MASQUERADE 2>/dev/null || true
    ip rule del fwmark 0xca11 table 100 2>/dev/null || true
    ip route flush table 100 2>/dev/null || true
    ip link del spectrune0 2>/dev/null || true
    rm -rf /sys/fs/cgroup/spectrune
    getent group spectrune >/dev/null && groupdel spectrune || true
fi

echo "Spectrune removed."
