//go:build linux && !gtk3

package main

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>

static void set_prefer_dark(gboolean dark) {
	GtkSettings *settings = gtk_settings_get_default();
	if (settings != NULL) {
		g_object_set(settings, "gtk-application-prefer-dark-theme", dark, NULL);
	}
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// applyWindowTheme makes GTK draw the window decorations (the title bar) in
// the desktop's light or dark style. GTK 4 draws them itself, but only
// libadwaita apps follow GNOME's dark-mode setting on their own: a plain GTK
// app like this one stays on the light Adwaita theme unless told otherwise.
// Wails reads the setting (IsDarkMode, ThemeChanged) but never hands it to
// GTK, so this does. The page content isn't affected: style.css pins it dark.
func applyWindowTheme(dark bool) {
	// GtkSettings belongs to the GTK main thread.
	application.InvokeAsync(func() {
		v := C.gboolean(0)
		if dark {
			v = 1
		}
		C.set_prefer_dark(v)
	})
}
