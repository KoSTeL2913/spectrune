#ifndef SPECTRUNE_GUI_WINDOW_LINUX_H
#define SPECTRUNE_GUI_WINDOW_LINUX_H
#include <gtk/gtk.h>
#ifdef GDK_WINDOWING_X11
#include <gdk/gdkx.h>
#endif

static void spectrune_present_window(GtkWidget *window) {
    gtk_widget_show(window);
    guint32 timestamp = gtk_get_current_event_time();
#ifdef GDK_WINDOWING_X11
    GdkWindow *native = gtk_widget_get_window(window);
    if (timestamp == GDK_CURRENT_TIME && native &&
        GDK_IS_X11_DISPLAY(gdk_window_get_display(native))) {
        timestamp = gdk_x11_get_server_time(native);
    }
#endif
    gtk_window_present_with_time(GTK_WINDOW(window), timestamp);
}
#endif
