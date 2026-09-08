// Native "browse for an .exe" file picker via the classic comdlg32
// GetOpenFileNameW — deliberately NOT the newer IFileOpenDialog COM API
// (which, like lxn/walk, tends to pull in comctl32/shell32 machinery) and
// not lxn/walk's FileDialog wrapper either. GetOpenFileNameW is a single
// blocking syscall with no window-class registration or tooltip
// involvement at all, so it can't hit the same failure mode.
package main

import (
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modcomdlg32          = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileNameW = modcomdlg32.NewProc("GetOpenFileNameW")
)

// openFileName mirrors the Win32 OPENFILENAMEW struct (only the fields we
// actually set; the rest are left zero, which is valid).
type openFileName struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        unsafe.Pointer
	dwReserved        uint32
	flagsEx           uint32
}

const (
	ofnFileMustExist = 0x00001000
	ofnPathMustExist = 0x00000800
	ofnExplorer      = 0x00080000
)

// filterString builds the double-NUL-terminated filter string
// GetOpenFileNameW expects, e.g. "Applications (*.exe)\x00*.exe\x00\x00".
func filterString(pairs ...string) *uint16 {
	var all []uint16
	for _, s := range pairs {
		all = append(all, utf16.Encode([]rune(s))...)
		all = append(all, 0)
	}
	all = append(all, 0)
	return &all[0]
}

// browseForFile shows the standard Windows "Open" dialog with the given
// title and filter (filterName/filterPattern, e.g. "Applications (*.exe)"
// / "*.exe") and returns the chosen path, or "" if the user canceled.
func browseForFile(title, filterName, filterPattern string) (string, error) {
	buf := make([]uint16, 32768) // long-path-safe

	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return "", err
	}

	ofn := openFileName{
		lpstrFilter: filterString(filterName, filterPattern),
		lpstrFile:   &buf[0],
		nMaxFile:    uint32(len(buf)),
		lpstrTitle:  titlePtr,
		flags:       ofnFileMustExist | ofnPathMustExist | ofnExplorer,
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))

	ret, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return "", nil // user canceled — not an error
	}
	return windows.UTF16ToString(buf), nil
}

// browseForExeFile shows the standard Windows "Open" dialog filtered to
// .exe files and returns the chosen path, or "" if the user canceled.
func browseForExeFile() (string, error) {
	return browseForFile("Select an application", "Applications (*.exe)", "*.exe")
}
