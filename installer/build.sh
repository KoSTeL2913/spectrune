#!/usr/bin/env bash
# Builds spectrune.exe + spectrune.msi for Windows.
#
# Exists because releases v2.0.41.0-v2.0.55.0 shipped a console-subsystem
# exe: the MSI packages ../spectrune.exe, and that copy was hand-built
# without -ldflags=-H=windowsgui, so Spectrune opened a console window next
# to the GUI (see cmd/spectrune/console_windows.go). This script refuses to
# package anything that isn't a GUI-subsystem exe.
set -euo pipefail
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "gofmt needed:" >&2
	echo "$unformatted" >&2
	exit 1
fi

go_ver=$(grep -oP 'appVersion = "\K[^"]+' cmd/spectrune/version.go)
msi_ver=$(grep -oP 'define ProductVersion = "\K[^"]+' installer/spectrune.wxs)
if [ "$go_ver" != "$msi_ver" ]; then
	echo "version mismatch: version.go=$go_ver spectrune.wxs=$msi_ver" >&2
	exit 1
fi

# Sync resources.rc's VERSIONINFO to appVersion and regenerate the .syso —
# a stale FILEVERSION lets MSI skip replacing the exe on upgrade (see the
# comment in resources.rc).
sed -i -E \
	-e "s/^(FILEVERSION|PRODUCTVERSION) .*/\1 ${go_ver//./,}/" \
	-e "s/(VALUE \"(FileVersion|ProductVersion)\", )\"[^\"]*\"/\1\"$go_ver\"/" \
	cmd/spectrune/resources.rc
(cd cmd/spectrune && x86_64-w64-mingw32-windres resources.rc -O coff -o resources_amd64.syso)

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-H=windowsgui ${EXTRA_LDFLAGS:-}" -o spectrune.exe ./cmd/spectrune

if ! file spectrune.exe | grep -q '(GUI)'; then
	echo "spectrune.exe is not a GUI-subsystem exe:" >&2
	file spectrune.exe >&2
	exit 1
fi

cp spectrune.exe installer/spectrune.exe
cd installer

# Fresh ProductCode + Component Guids on every build (release checklist
# at the top of spectrune.wxs) — a reused component GUID lets Windows
# Installer skip copying spectrune.exe. UpgradeCode must never change.
new_guid() { uuidgen | tr a-z A-Z; }
sed -i -E "s/(define ProductCode = )\"[^\"]*\"/\1\"$(new_guid)\"/" spectrune.wxs
for c in SpectruneExeComponent DesktopShortcutComponent StartMenuShortcutComponent; do
	sed -i -E "s/(Component Id=\"$c\" Guid=)\"[^\"]*\"/\1\"$(new_guid)\"/" spectrune.wxs
done

wixl --ext ui -a x64 spectrune.wxs -o spectrune.msi
echo "built spectrune.msi $msi_ver"
