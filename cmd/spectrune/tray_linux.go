// System tray icon — the Linux analog of tray_windows.go's Shell_NotifyIcon
// based implementation, using GtkStatusIcon (deprecated since GTK 3.14 but
// still functional on X11/Cinnamon, which is what this was built and
// tested against — GNOME Shell dropped status-icon support natively and
// needs an extension; Wayland-only setups may not show it at all. GTK is
// already linked in for webview_go/icons_linux.go, so this adds no new
// build dependency, just more of the same library.
package main

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>
#include <stdlib.h>

extern void goTrayActivate(GtkStatusIcon*, gpointer);
extern void goTrayPopupMenu(GtkStatusIcon*, guint, guint32, gpointer);
extern void goTrayMenuItem(GtkMenuItem*, gpointer);
extern gboolean goWindowDeleteEvent(GtkWidget*, GdkEvent*, gpointer);

// spectrune_hide_to_tray_on_close intercepts the window manager's close
// button (delete-event) and hides the window instead of letting GTK
// destroy it — the Linux analog of tray_windows.go's hideToTrayOnClose
// window-subclassing trick, just via a normal GTK signal instead of a
// Win32 WNDPROC swap.
static void spectrune_hide_to_tray_on_close(GtkWidget *win) {
	g_signal_connect(win, "delete-event", G_CALLBACK(goWindowDeleteEvent), NULL);
}

static void spectrune_window_show(GtkWidget *win) {
	gtk_widget_show(win);
	gtk_window_present(GTK_WINDOW(win));
}

static GtkStatusIcon* spectrune_tray_new(const char *iconPath) {
	GtkStatusIcon *icon = gtk_status_icon_new_from_file(iconPath);
	gtk_status_icon_set_visible(icon, TRUE);
	g_signal_connect(icon, "activate", G_CALLBACK(goTrayActivate), NULL);
	g_signal_connect(icon, "popup-menu", G_CALLBACK(goTrayPopupMenu), NULL);
	return icon;
}

static void spectrune_tray_set_icon(GtkStatusIcon *icon, const char *path) {
	gtk_status_icon_set_from_file(icon, path);
}

static void spectrune_tray_set_tooltip(GtkStatusIcon *icon, const char *text) {
	gtk_status_icon_set_tooltip_text(icon, text);
}

// spectrune_menu_add appends one item to menu, wired to goTrayMenuItem with
// the given int id passed back through the closure data pointer. Passing a
// small heap-allocated int* (freed by the Go-side callback) rather than
// stuffing the id into the pointer's bit pattern directly — simpler to get
// right across the cgo boundary.
static GtkWidget* spectrune_menu_add(GtkWidget *menu, const char *label, int enabled, int id) {
	GtkWidget *item = gtk_menu_item_new_with_label(label);
	gtk_widget_set_sensitive(item, enabled);
	gtk_menu_shell_append(GTK_MENU_SHELL(menu), item);
	if (enabled) {
		int *idPtr = malloc(sizeof(int));
		*idPtr = id;
		g_signal_connect(item, "activate", G_CALLBACK(goTrayMenuItem), idPtr);
	}
	gtk_widget_show(item);
	return item;
}

static void spectrune_menu_add_separator(GtkWidget *menu) {
	GtkWidget *sep = gtk_separator_menu_item_new();
	gtk_menu_shell_append(GTK_MENU_SHELL(menu), sep);
	gtk_widget_show(sep);
}

static void spectrune_menu_popup(GtkWidget *menu, GtkStatusIcon *icon, guint button, guint32 activateTime) {
	gtk_menu_popup(GTK_MENU(menu), NULL, NULL, gtk_status_icon_position_menu, icon, button, activateTime);
}
*/
import "C"

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/webview/webview_go"
)

//go:embed assets/tray-active.png
var trayActivePNG []byte

//go:embed assets/tray-inactive.png
var trayInactivePNG []byte

type trayIcon struct {
	w                        webview.WebView
	gtkIcon                  *C.GtkStatusIcon
	onShow                   func()
	onExit                   func()
	activePath, inactivePath string

	mu           sync.Mutex
	menuProfiles []string // index -> profile name, valid while a menu built from fetchMenuState is open
}

var currentTray *trayIcon // single instance — this app never has more than one tray icon

// writeEmbeddedIcon extracts an embedded PNG to a stable file path —
// GtkStatusIcon's simple file-based API needs a real path, not bytes.
func writeEmbeddedIcon(name string, data []byte) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "spectrune")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// newTrayIcon creates the status icon. Must be called on the GTK main
// thread — from runGUI(), before w.Run(), same requirement webview_go's
// own setup has.
func newTrayIcon(w webview.WebView, onShow, onExit func()) (*trayIcon, error) {
	activePath, err := writeEmbeddedIcon("tray-active.png", trayActivePNG)
	if err != nil {
		return nil, err
	}
	inactivePath, err := writeEmbeddedIcon("tray-inactive.png", trayInactivePNG)
	if err != nil {
		return nil, err
	}

	cPath := C.CString(inactivePath)
	defer C.free(unsafe.Pointer(cPath))
	gtkIcon := C.spectrune_tray_new(cPath)
	if gtkIcon == nil {
		return nil, fmt.Errorf("gtk_status_icon_new_from_file failed")
	}

	t := &trayIcon{
		w: w, gtkIcon: gtkIcon, onShow: onShow, onExit: onExit,
		activePath: activePath, inactivePath: inactivePath,
	}
	currentTray = t
	return t, nil
}

// SetState updates the tray glyph + tooltip — same contract as
// tray_windows.go's SetState, dispatched onto the GTK main thread since
// this is typically called from the background poll goroutine
// (pollTrayState) and GTK objects aren't safe to touch from elsewhere.
func (t *trayIcon) SetState(connected bool, profileName string) {
	path := t.inactivePath
	tip := "Spectrune — disconnected"
	if connected {
		path = t.activePath
		tip = fmt.Sprintf("Spectrune — connected: %s", profileName)
	}
	t.w.Dispatch(func() {
		cPath := C.CString(path)
		defer C.free(unsafe.Pointer(cPath))
		C.spectrune_tray_set_icon(t.gtkIcon, cPath)
		cTip := C.CString(tip)
		defer C.free(unsafe.Pointer(cTip))
		C.spectrune_tray_set_tooltip(t.gtkIcon, cTip)
	})
}

// fetchMenuState mirrors tray_windows.go's own helper — one State +
// ListProfiles round trip per menu open.
func (t *trayIcon) fetchMenuState() (profiles []string, connected bool, connectedProfile string) {
	client, err := ipcDial()
	if err != nil {
		return nil, false, ""
	}
	defer client.Close()
	var state StateReply
	client.Call("Bridge.State", struct{}{}, &state)
	var names []string
	client.Call("Bridge.ListProfiles", struct{}{}, &names)
	return names, state.Connected, state.ProfileName
}

const (
	menuIDShow = 1000000 + iota
	menuIDExit
	menuIDDisconnect
	menuIDConnectBase // profile items use menuIDConnectBase+index
)

// buildAndShowMenu runs on the GTK main thread (called directly from the
// popup-menu signal handler, already on that thread — see goTrayPopupMenu).
func (t *trayIcon) buildAndShowMenu(button C.guint, activateTime C.guint32) {
	profiles, connected, connectedProfile := t.fetchMenuState()

	t.mu.Lock()
	t.menuProfiles = profiles
	t.mu.Unlock()

	menu := C.gtk_menu_new()

	switch {
	case connected:
		label := fmt.Sprintf("Disconnect (%s)", connectedProfile)
		cLabel := C.CString(label)
		C.spectrune_menu_add(menu, cLabel, 1, C.int(menuIDDisconnect))
		C.free(unsafe.Pointer(cLabel))
	case len(profiles) > 0:
		for i, name := range profiles {
			cLabel := C.CString(name)
			C.spectrune_menu_add(menu, cLabel, 1, C.int(menuIDConnectBase+i))
			C.free(unsafe.Pointer(cLabel))
		}
	default:
		cLabel := C.CString("No profiles")
		C.spectrune_menu_add(menu, cLabel, 0, 0)
		C.free(unsafe.Pointer(cLabel))
	}

	C.spectrune_menu_add_separator(menu)
	cShow := C.CString("Show")
	C.spectrune_menu_add(menu, cShow, 1, C.int(menuIDShow))
	C.free(unsafe.Pointer(cShow))
	cExit := C.CString("Exit")
	C.spectrune_menu_add(menu, cExit, 1, C.int(menuIDExit))
	C.free(unsafe.Pointer(cExit))

	C.spectrune_menu_popup(menu, t.gtkIcon, button, activateTime)
}

func (t *trayIcon) handleMenuItem(id int) {
	switch {
	case id == menuIDShow:
		if t.onShow != nil {
			t.onShow()
		}
	case id == menuIDExit:
		if t.onExit != nil {
			t.onExit()
		}
	case id == menuIDDisconnect:
		go func() {
			client, err := ipcDial()
			if err != nil {
				return
			}
			defer client.Close()
			client.Call("Bridge.Disconnect", struct{}{}, &struct{}{})
		}()
	case id >= menuIDConnectBase:
		t.mu.Lock()
		idx := id - menuIDConnectBase
		var name string
		if idx >= 0 && idx < len(t.menuProfiles) {
			name = t.menuProfiles[idx]
		}
		t.mu.Unlock()
		if name != "" {
			go quickToggleProfile(name)
		}
	}
}

//export goTrayActivate
func goTrayActivate(icon *C.GtkStatusIcon, data C.gpointer) {
	if currentTray != nil && currentTray.onShow != nil {
		currentTray.onShow()
	}
}

//export goWindowDeleteEvent
func goWindowDeleteEvent(widget *C.GtkWidget, event *C.GdkEvent, data C.gpointer) C.gboolean {
	C.gtk_widget_hide(widget)
	return C.TRUE // stop the default handler — don't destroy the window
}

// hideToTrayOnClose wires win's close button to hide instead of exit.
func hideToTrayOnClose(win unsafe.Pointer) {
	C.spectrune_hide_to_tray_on_close((*C.GtkWidget)(win))
}

// showWindow un-hides and raises win — used for the tray's Show action
// and a second /gui launch's activate-existing-window path.
func showWindow(win unsafe.Pointer) {
	C.spectrune_window_show((*C.GtkWidget)(win))
}

//export goTrayPopupMenu
func goTrayPopupMenu(icon *C.GtkStatusIcon, button C.guint, activateTime C.guint32, data C.gpointer) {
	if currentTray != nil {
		currentTray.buildAndShowMenu(button, activateTime)
	}
}

//export goTrayMenuItem
func goTrayMenuItem(item *C.GtkMenuItem, data C.gpointer) {
	idPtr := (*C.int)(unsafe.Pointer(data))
	id := int(*idPtr)
	C.free(unsafe.Pointer(idPtr))
	if currentTray != nil {
		currentTray.handleMenuItem(id)
	}
}

// trayPollInterval mirrors tray_windows.go's own poll cadence.
const trayPollInterval = 3 * time.Second

// pollTrayState mirrors tray_windows.go's own poll loop — updates the
// tray glyph from live daemon state on trayPollInterval, until stop fires.
func pollTrayState(t *trayIcon, stop <-chan struct{}) {
	ticker := time.NewTicker(trayPollInterval)
	defer ticker.Stop()
	for {
		client, err := ipcDial()
		if err == nil {
			var state StateReply
			if err := client.Call("Bridge.State", struct{}{}, &state); err == nil {
				t.SetState(state.Connected && state.HandshakeOK, state.ProfileName)
			}
			client.Close()
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}
