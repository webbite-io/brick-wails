# dmgbuild settings for the release DMG; run by make-dmg.sh, which passes
# the paths below with -D. The window's size and the icon positions have to
# agree with background.swift, which draws the title and the arrow around them.

import os.path

app = defines["app"]  # noqa: F821 (dmgbuild provides defines)
app_name = os.path.basename(app)

files = [app]
symlinks = {"Applications": "/Applications"}
hide_extension = [app_name]

# The volume's icon, which Finder also shows in the window's title bar.
icon = defines["volume_icon"]  # noqa: F821

background = defines["background"]  # noqa: F821
window_rect = ((200, 160), (660, 380))
default_view = "icon-view"
show_status_bar = False
show_tab_view = False
show_toolbar = False
show_pathbar = False
show_sidebar = False
show_icon_preview = False
arrange_by = None
icon_size = 128
text_size = 14
icon_locations = {
    app_name: (180, 205),
    "Applications": (480, 205),
}

format = "UDZO"
filesystem = "HFS+"
