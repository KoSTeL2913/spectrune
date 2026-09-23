// Colors the native window title bar to match the app's current accent
// color (webui_html.go's theme picker) instead of leaving it whatever
// stock color Windows uses — requested by the user 2026-09-23. Only takes
// effect on Windows 11 (build 22000+); DwmSetWindowAttribute simply
// returns an error for DWMWA_CAPTION_COLOR/DWMWA_TEXT_COLOR on Windows 10,
// which setTitleBarColor swallows rather than surfacing as a user-visible
// failure — an uncolored title bar there is the correct, unsurprising
// fallback, not a bug to report.
package main

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modDwmapi                 = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = modDwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	dwmwaBorderColor  = 34
	dwmwaCaptionColor = 35
	dwmwaTextColor    = 36
)

// parseHexColor turns "#rrggbb" (or "rrggbb") into a COLORREF — Win32's
// 0x00BBGGRR byte order, the reverse of the CSS hex string's RRGGBB.
func parseHexColor(hex string) (uint32, error) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, fmt.Errorf("parseHexColor: %q is not a 6-digit hex color", hex)
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("parseHexColor: %w", err)
	}
	r := (v >> 16) & 0xff
	g := (v >> 8) & 0xff
	b := v & 0xff
	return uint32(b<<16 | g<<8 | r), nil
}

// luminanceTextColor picks black or white text — whichever reads clearly
// against bg — using the standard relative-luminance threshold rather
// than hardcoding white (a bright accent like the light-yellow theme
// swatch would otherwise get white-on-yellow, unreadable).
func luminanceTextColor(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return "#ffffff"
	}
	r := float64((v>>16)&0xff) / 255
	g := float64((v>>8)&0xff) / 255
	b := float64(v&0xff) / 255
	// Perceived-brightness weighting (ITU-R BT.601), not a plain average —
	// matches how the eye actually weighs green over blue/red.
	luma := 0.299*r + 0.587*g + 0.114*b
	if luma > 0.6 {
		return "#000000"
	}
	return "#ffffff"
}

func setTitleBarColor(hwnd uintptr, hex string) error {
	colorRef, err := parseHexColor(hex)
	if err != nil {
		return err
	}
	textColorRef, err := parseHexColor(luminanceTextColor(hex))
	if err != nil {
		return err
	}
	if err := dwmSetWindowAttribute(hwnd, dwmwaCaptionColor, colorRef); err != nil {
		// Expected on Windows 10 — DWMWA_CAPTION_COLOR is an 11-only
		// attribute. Not worth logging on every theme change.
		return nil
	}
	// Border color is cosmetic (a thin accent-colored outline) and text
	// color only matters once the caption itself actually took — both
	// best-effort past this point, worth trying but not worth failing
	// setTitleBarColor as a whole over.
	_ = dwmSetWindowAttribute(hwnd, dwmwaTextColor, textColorRef)
	_ = dwmSetWindowAttribute(hwnd, dwmwaBorderColor, colorRef)
	return nil
}

func dwmSetWindowAttribute(hwnd uintptr, attr uint32, value uint32) error {
	ret, _, _ := procDwmSetWindowAttribute.Call(
		hwnd,
		uintptr(attr),
		uintptr(unsafe.Pointer(&value)),
		unsafe.Sizeof(value),
	)
	if ret != 0 { // HRESULT — 0 (S_OK) is the only success value
		return fmt.Errorf("DwmSetWindowAttribute(attr=%d): HRESULT 0x%x", attr, uint32(ret))
	}
	return nil
}
