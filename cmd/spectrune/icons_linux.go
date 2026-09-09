// App icon resolution for the Apps picker — the Linux analog of
// icons_windows.go's SHGetFileInfoW-based extraction, using GTK's icon
// theme API instead (correctly handles theme inheritance, hicolor
// fallback, symbolic/scalable icons — a hand-rolled directory search
// would just be reimplementing a chunk of the freedesktop icon theme
// spec badly). No extra build dependency: GTK is already linked in for
// webview_go (webui_linux.go), this just uses a bit more of it.
package main

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>
#include <stdlib.h>

static char* spectrune_lookup_icon_file(const char* name, int size) {
	GtkIconTheme *theme = gtk_icon_theme_get_default();
	GtkIconInfo *info = gtk_icon_theme_lookup_icon(theme, name, size, GTK_ICON_LOOKUP_USE_BUILTIN);
	if (!info) {
		return NULL;
	}
	const char *filename = gtk_icon_info_get_filename(info);
	char *result = filename ? strdup(filename) : NULL;
	g_object_unref(info);
	return result;
}
*/
import "C"

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

const iconLookupSize = 64

// getAppIconDataURI resolves a .desktop Icon= value (either an absolute
// path or a theme icon name, both are valid per the freedesktop spec) to
// a data: URI the GUI can drop straight into an <img> tag.
func getAppIconDataURI(icon string) (string, error) {
	if icon == "" {
		return "", nil
	}

	path := icon
	if !filepath.IsAbs(icon) {
		resolved, err := lookupThemeIconFile(icon)
		if err != nil || resolved == "" {
			return "", nil // no icon found — not an error, the GUI just shows a blank one
		}
		path = resolved
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	return fileToDataURI(path, data), nil
}

func lookupThemeIconFile(name string) (string, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	cPath := C.spectrune_lookup_icon_file(cName, C.int(iconLookupSize))
	if cPath == nil {
		return "", nil
	}
	defer C.free(unsafe.Pointer(cPath))
	return C.GoString(cPath), nil
}

func fileToDataURI(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	mime := "image/png"
	switch ext {
	case ".svg":
		mime = "image/svg+xml"
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".xpm":
		mime = "image/x-xpixmap"
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data))
}
