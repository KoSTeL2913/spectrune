package main

/*
#cgo pkg-config: gtk+-3.0
#include "gui_window_linux.h"
#include <stdlib.h>

typedef struct {
    GApplication *app;
    GtkWidget *window;
    gboolean pending;
} SpectruneGUIInstance;

static void spectrune_gui_activate(GApplication *app, gpointer data) {
    SpectruneGUIInstance *instance = data;
    if (!instance->window) {
        instance->pending = TRUE;
        return;
    }
    spectrune_present_window(instance->window);
}

static SpectruneGUIInstance *spectrune_gui_register(char **message) {
    SpectruneGUIInstance *instance = g_new0(SpectruneGUIInstance, 1);
    instance->app = g_application_new("io.github.KoSTeL2913.Spectrune", 0);
    g_signal_connect(instance->app, "activate", G_CALLBACK(spectrune_gui_activate), instance);
    GError *error = NULL;
    if (!g_application_register(instance->app, NULL, &error)) {
        *message = g_strdup(error ? error->message : "session registration failed");
        g_clear_error(&error);
        g_object_unref(instance->app);
        g_free(instance);
        return NULL;
    }
    if (!g_application_get_dbus_connection(instance->app)) {
        *message = g_strdup("desktop session bus is unavailable");
        g_object_unref(instance->app);
        g_free(instance);
        return NULL;
    }
    return instance;
}

static gboolean spectrune_gui_remote(SpectruneGUIInstance *instance, char **message) {
    if (!g_application_get_is_remote(instance->app)) return FALSE;
    g_application_activate(instance->app);
    GError *error = NULL;
    if (!g_dbus_connection_flush_sync(g_application_get_dbus_connection(instance->app), NULL, &error)) {
        *message = g_strdup(error->message);
        g_error_free(error);
    }
    return TRUE;
}

static void spectrune_gui_attach(SpectruneGUIInstance *instance, void *window) {
    instance->window = GTK_WIDGET(window);
    if (instance->pending) {
        instance->pending = FALSE;
        spectrune_gui_activate(instance->app, instance);
    }
}

static void spectrune_gui_release(SpectruneGUIInstance *instance) {
    instance->window = NULL;
    g_signal_handlers_disconnect_by_data(instance->app, instance);
    g_object_unref(instance->app);
    g_free(instance);
}
*/
import "C"

import (
	"fmt"
	"time"
	"unsafe"
)

type linuxGUIInstance struct{ native *C.SpectruneGUIInstance }

// The session bus arbitrates ownership before a WebView is constructed.
// A repeated launch only sends Activate to the existing GTK main loop.
func acquireLinuxGUIInstance(retry bool) (*linuxGUIInstance, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		var message *C.char
		native := C.spectrune_gui_register(&message)
		if message != nil {
			defer C.g_free(C.gpointer(message))
		}
		if native == nil {
			return nil, fmt.Errorf("single GUI instance: %s", C.GoString(message))
		}
		if retry && C.g_application_get_is_remote(native.app) != 0 && time.Now().Before(deadline) {
			C.spectrune_gui_release(native)
			time.Sleep(150 * time.Millisecond)
			continue
		}
		var activationError *C.char
		if C.spectrune_gui_remote(native, &activationError) != 0 {
			C.spectrune_gui_release(native)
			if activationError != nil {
				defer C.g_free(C.gpointer(activationError))
				return nil, fmt.Errorf("activate existing GUI: %s", C.GoString(activationError))
			}
			return nil, nil
		}
		return &linuxGUIInstance{native: native}, nil
	}
}
func (instance *linuxGUIInstance) attach(window unsafe.Pointer) {
	C.spectrune_gui_attach(instance.native, window)
}
func (instance *linuxGUIInstance) close() { C.spectrune_gui_release(instance.native) }
