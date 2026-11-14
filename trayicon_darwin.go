//go:build darwin

package main

// trayIconScale shrinks the tray icon within the menu bar, which stretches
// its image to the bar's full height: at full size the Brick glyph looks
// larger than the system's own icons.
const trayIconScale = 0.9
