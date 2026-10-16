//go:build !linux || gtk3

package main

// applyWindowTheme is for GTK 4 on Linux: Windows and macOS give the title
// bar the system's light or dark style without help.
func applyWindowTheme(bool) {}
