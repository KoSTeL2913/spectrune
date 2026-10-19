// Repairs the icon of taskbar/Start pins of Spectrune made by older
// installers. Up to v2.0.58.0 the MSI shortcuts took their icon from
// C:\Windows\Installer\{ProductCode}\AppIcon; the ProductCode changes every
// build and the old folder is removed on upgrade. The installer rewrites
// its own Desktop/Start Menu shortcuts, but a pin is the user's own copy
// that nothing rewrites, so it kept pointing at the deleted folder and
// showed a blank icon as soon as Explorer's icon cache was rebuilt
// (confirmed on mainpc 2026-10-18). Since v2.0.59.0 the shortcuts have no
// Icon= and use spectrune.exe's own icon; this fixes existing pins the same
// way. Minimal hand-rolled IShellLinkW/IPersistFile vtable calls — the rest
// of the app has no COM dependency and this doesn't justify adding one.
package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modole32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modole32.NewProc("CoCreateInstance")
	procSHChangeNotify   = modshell32.NewProc("SHChangeNotify")

	clsidShellLink  = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// vtable slots (IUnknown takes 0-2).
const (
	vtQueryInterface  = 0
	vtRelease         = 2
	slGetPath         = 3
	slGetIconLocation = 16
	slSetIconLocation = 17
	pfLoad            = 5
	pfSave            = 6

	clsctxInprocServer = 1
	coinitApartment    = 2
	stgmReadWrite      = 2
	slgpRawPath        = 4
	shcneUpdateItem    = 0x2000
	shcneAssocChanged  = 0x08000000
	shcnfIDList        = 0
	shcnfPathW         = 5
	pathBufLen         = 1024
)

func comCall(obj uintptr, slot int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func repairPinnedShortcutIcons() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return
	}
	pinned := filepath.Join(appData, `Microsoft\Internet Explorer\Quick Launch\User Pinned`)
	var lnks []string
	for _, sub := range []string{"TaskBar", "StartMenu"} {
		m, _ := filepath.Glob(filepath.Join(pinned, sub, "*.lnk"))
		lnks = append(lnks, m...)
	}
	if len(lnks) == 0 {
		return
	}

	// COM wants the same OS thread from CoInitializeEx to CoUninitialize.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, coinitApartment); err != nil {
		return
	}
	defer windows.CoUninitialize()

	for _, lnk := range lnks {
		if repairPinnedShortcutIcon(lnk, exe) {
			log.Printf("repaired icon of pinned shortcut %s", lnk)
		}
	}
}

func repairPinnedShortcutIcon(lnk, exe string) bool {
	var sl uintptr
	if r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&sl))); r != 0 || sl == 0 {
		return false
	}
	defer comCall(sl, vtRelease)
	var pf uintptr
	if comCall(sl, vtQueryInterface, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&pf))) != 0 || pf == 0 {
		return false
	}
	defer comCall(pf, vtRelease)

	lnkW, _ := windows.UTF16PtrFromString(lnk)
	if comCall(pf, pfLoad, uintptr(unsafe.Pointer(lnkW)), stgmReadWrite) != 0 {
		return false
	}
	buf := make([]uint16, pathBufLen)
	if comCall(sl, slGetPath, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, slgpRawPath) != 0 {
		return false
	}
	target := expandEnv(windows.UTF16ToString(buf))
	if !strings.EqualFold(filepath.Clean(target), filepath.Clean(exe)) {
		return false
	}
	var idx int32
	if comCall(sl, slGetIconLocation, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&idx))) != 0 {
		return false
	}
	icon := windows.UTF16ToString(buf)
	if icon == "" {
		return false
	}
	if _, err := os.Stat(expandEnv(icon)); err == nil {
		return false
	}

	exeW, _ := windows.UTF16PtrFromString(exe)
	if comCall(sl, slSetIconLocation, uintptr(unsafe.Pointer(exeW)), 0) != 0 {
		return false
	}
	// Save(NULL, TRUE) writes back to the file it was loaded from.
	if comCall(pf, pfSave, 0, 1) != 0 {
		return false
	}
	procSHChangeNotify.Call(shcneUpdateItem, shcnfPathW, uintptr(unsafe.Pointer(lnkW)), 0)
	return true
}

// refreshShellIconsAfterUpdate makes Explorer drop its cached icons once
// per installed version. The cache is keyed by file path, and the exe path
// never changes across updates, so after v2.0.60.0 replaced the logo the
// desktop shortcut and the taskbar button kept showing the old one
// (confirmed on win11 2026-10-19) until something flushed the cache.
// Must run before the window is created: the taskbar looks the icon up
// when the button first appears.
func refreshShellIconsAfterUpdate() {
	dir, err := os.UserCacheDir() // %LocalAppData%
	if err != nil {
		return
	}
	marker := filepath.Join(dir, "Spectrune", "icons-refreshed")
	if b, err := os.ReadFile(marker); err == nil && string(b) == appVersion {
		return
	}
	procSHChangeNotify.Call(shcneAssocChanged, shcnfIDList, 0, 0)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err == nil {
		os.WriteFile(marker, []byte(appVersion), 0o600)
	}
}

// expandEnv expands %VAR% references, which shortcut paths may contain.
func expandEnv(s string) string {
	if e, err := registry.ExpandString(s); err == nil {
		return e
	}
	return s
}
