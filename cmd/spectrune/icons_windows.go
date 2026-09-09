// Extracts a small icon for an installed app's .exe as a base64 PNG data
// URI, for the web UI's app picker list. Pure GDI/shell32 — SHGetFileInfo
// + GetIconInfo + GetDIBits, no comctl32/common-controls involvement at
// all, so this can't hit the tooltip-init failure that killed the
// lxn/walk GUI attempt.
package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modshell32          = windows.NewLazySystemDLL("shell32.dll")
	procSHGetFileInfoW  = modshell32.NewProc("SHGetFileInfoW")
	procGetIconInfo     = moduser32.NewProc("GetIconInfo")
	procDestroyIcon     = moduser32.NewProc("DestroyIcon")
	procGetDC           = moduser32.NewProc("GetDC")
	procReleaseDC       = moduser32.NewProc("ReleaseDC")
	modgdi32            = windows.NewLazySystemDLL("gdi32.dll")
	procGetObjectW      = modgdi32.NewProc("GetObjectW")
	procGetDIBits       = modgdi32.NewProc("GetDIBits")
	procDeleteObjectGDI = modgdi32.NewProc("DeleteObject")
)

const (
	shgfiIcon      = 0x000000100
	shgfiSmallIcon = 0x000000001
)

type shFileInfoW struct {
	hIcon         windows.Handle
	iIcon         int32
	dwAttributes  uint32
	szDisplayName [260]uint16
	szTypeName    [80]uint16
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  windows.Handle
	hbmColor windows.Handle
}

type gdiBitmap struct {
	Type       int32
	Width      int32
	Height     int32
	WidthBytes int32
	Planes     uint16
	BitsPixel  uint16
	Bits       unsafe.Pointer
}

type biHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// getAppIconDataURI returns a "data:image/png;base64,..." URI for path's
// small shell icon, or an error if the file/icon can't be resolved.
func getAppIconDataURI(path string) (string, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}

	var shfi shFileInfoW
	ret, _, _ := procSHGetFileInfoW.Call(
		uintptr(unsafe.Pointer(pathPtr)), 0,
		uintptr(unsafe.Pointer(&shfi)), unsafe.Sizeof(shfi),
		shgfiIcon|shgfiSmallIcon)
	if ret == 0 || shfi.hIcon == 0 {
		return "", fmt.Errorf("SHGetFileInfo(%s): no icon", path)
	}
	defer procDestroyIcon.Call(uintptr(shfi.hIcon))

	var ii iconInfo
	if r, _, _ := procGetIconInfo.Call(uintptr(shfi.hIcon), uintptr(unsafe.Pointer(&ii))); r == 0 {
		return "", fmt.Errorf("GetIconInfo(%s) failed", path)
	}
	if ii.hbmMask != 0 {
		defer procDeleteObjectGDI.Call(uintptr(ii.hbmMask))
	}
	if ii.hbmColor != 0 {
		defer procDeleteObjectGDI.Call(uintptr(ii.hbmColor))
	}
	if ii.hbmColor == 0 {
		return "", fmt.Errorf("icon for %s has no color bitmap (monochrome cursor?)", path)
	}

	var bmp gdiBitmap
	procGetObjectW.Call(uintptr(ii.hbmColor), unsafe.Sizeof(bmp), uintptr(unsafe.Pointer(&bmp)))
	w, h := int(bmp.Width), int(bmp.Height)
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("icon for %s has invalid size %dx%d", path, w, h)
	}

	hdc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, hdc)

	bi := struct {
		Header biHeader
	}{Header: biHeader{
		Size:        uint32(unsafe.Sizeof(biHeader{})),
		Width:       int32(w),
		Height:      int32(-h), // negative = top-down rows, simpler to decode
		Planes:      1,
		BitCount:    32,
		Compression: 0, // BI_RGB
	}}
	buf := make([]byte, w*h*4)
	if r, _, _ := procGetDIBits.Call(hdc, uintptr(ii.hbmColor), 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0 /* DIB_RGB_COLORS */); r == 0 {
		return "", fmt.Errorf("GetDIBits(%s) failed", path)
	}

	// Many legacy 32-bit icon resources leave alpha at 0 for every pixel
	// (they rely on hbmMask for transparency instead of a real alpha
	// channel). Decide this once for the whole image, not per pixel — a
	// genuinely transparent modern icon has a MIX of zero and non-zero
	// alpha, whereas a legacy all-zero-alpha icon has none at all.
	hasAlpha := false
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0 {
			hasAlpha = true
			break
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			b, g, r, a := buf[i], buf[i+1], buf[i+2], buf[i+3]
			if !hasAlpha {
				a = 255
			}
			img.Set(x, y, color.RGBA{r, g, b, a})
		}
	}

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBuf.Bytes()), nil
}
