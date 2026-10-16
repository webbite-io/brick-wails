#!/usr/bin/env bash
#
# build-appimage.sh - Package the app as an AppImage with its own WebKit
#
# Run by `make release` on Linux, after the production build has put
# bin/brick-ui in place. Follows `wails3 generate appimage` — an AppDir with
# AppImageKit's AppRun, WebKitGTK's out-of-process helpers copied in, then
# linuxdeploy with wails' GTK plugin to bundle the libraries — differing where
# that leaves the AppImage running the system's WebKit helpers:
#
# 1. Only WebKitGTK 6.0's helpers are bundled. wails3 copies every
#    WebKitWebProcess it finds under /usr, so a build host that also has
#    webkit2gtk-4.1 installed ships that WebKit and the GTK3 stack beneath it,
#    which the GTK4 app never loads.
#
# 2. libwebkitgtk starts its helpers from a directory compiled into it, e.g.
#    /usr/lib/x86_64-linux-gnu/webkitgtk-6.0, so the bundled library would
#    start the *system's* helpers. When the user's WebKitGTK differs from the
#    build host's, the web process crashes on start and every window stays
#    black; with no WebKitGTK installed there are no helpers to start at all.
#    (WEBKIT_EXEC_PATH would override the directory, but release builds of
#    WebKit ignore it.) So the path is rewritten in the bundled library to one
#    of the same length relative to the working directory — "/usr/lib/…"
#    becomes "././/lib/…" — leaving the binary layout untouched. AppRun changes
#    into $APPDIR/usr before starting the app, which is where the relative path
#    then lands. Tauri patches its AppImages the same way.
#
# 3. The helpers keep their execute bit; wails3 copies them without it.
#
# Usage:
#   build-appimage.sh <binary> <icon> <desktop file> <GTK plugin> <build dir> <output AppImage>
#
# The icon has to be named after the desktop file's Icon= key (linuxdeploy
# matches it by basename). The GTK plugin is linuxdeploy-plugin-gtk.sh from the
# wails module at the version go.mod pins. linuxdeploy and AppRun are
# downloaded from GitHub on first run and cached in the build dir, which also
# holds the AppDir.

set -euo pipefail

if [ $# -ne 6 ]; then
	echo "Usage: $0 <binary> <icon> <desktop file> <GTK plugin> <build dir> <output AppImage>" >&2
	exit 1
fi

BINARY="$(realpath "$1")"
ICON="$(realpath "$2")"
DESKTOP_FILE="$(realpath "$3")"
GTK_PLUGIN="$(realpath "$4")"
BUILD_DIR="$(realpath -m "$5")"
OUTPUT="$(realpath -m "$6")"

for f in "$BINARY" "$ICON" "$DESKTOP_FILE" "$GTK_PLUGIN"; do
	[ -f "$f" ] || { echo "Error: $f not found" >&2; exit 1; }
done

ARCH="$(uname -m)"
case "$ARCH" in
x86_64 | aarch64) ;;
*) echo "Error: unsupported architecture $ARCH" >&2; exit 1 ;;
esac

NAME="$(basename "$BINARY")"
APPDIR="$BUILD_DIR/$NAME-$ARCH.AppDir"
LINUXDEPLOY="$BUILD_DIR/linuxdeploy-$ARCH.AppImage"
APPRUN="$BUILD_DIR/AppRun-$ARCH"

# Run a step quietly, showing its output only when it fails.
quiet() {
	local log
	log="$(mktemp)"
	if ! "$@" >"$log" 2>&1; then
		cat "$log" >&2
		rm -f "$log"
		return 1
	fi
	rm -f "$log"
}

download() {
	if [ ! -f "$2" ]; then
		echo "  Downloading $(basename "$1")..."
		curl -fsSL -o "$2.tmp" "$1"
		mv "$2.tmp" "$2"
	fi
	chmod 755 "$2"
}

mkdir -p "$BUILD_DIR"
download "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-$ARCH.AppImage" "$LINUXDEPLOY"
download "https://github.com/AppImage/AppImageKit/releases/download/continuous/AppRun-$ARCH" "$APPRUN"

# Extract linuxdeploy once rather than --appimage-extract-and-run each time, so
# its AppImage plugin can also be run on its own below.
extracted="$BUILD_DIR/linuxdeploy-$ARCH-extracted"
if [ ! -d "$extracted" ] || [ "$LINUXDEPLOY" -nt "$extracted" ]; then
	rm -rf "$extracted"
	tmp="$(mktemp -d -p "$BUILD_DIR")"
	(cd "$tmp" && "$LINUXDEPLOY" --appimage-extract >/dev/null)
	mv "$tmp/squashfs-root" "$extracted"
	rm -rf "$tmp"
fi

echo "  Preparing $(basename "$APPDIR")..."
rm -rf "$APPDIR"
install -D -m 755 "$BINARY" "$APPDIR/usr/bin/$NAME"
install -m 755 "$APPRUN" "$APPDIR/AppRun"
cp "$ICON" "$APPDIR/.DirIcon"
ln -s .DirIcon "$APPDIR/$(basename "$ICON")"
cp "$DESKTOP_FILE" "$APPDIR/"

# WebKitGTK 6.0's helpers and injected bundle, at the same path under the
# AppDir's usr/ as under the system's /usr (they live in different places per
# distro: /usr/lib/<triplet>/ on Debian, /usr/libexec/ or /usr/lib64/
# elsewhere). linuxdeploy then bundles what they link against.
helper="$(find /usr -path '*/webkitgtk-6.0/WebKitWebProcess' -type f -print -quit 2>/dev/null)"
bundle="$(find /usr -path '*/webkitgtk-6.0/injected-bundle/libwebkitgtkinjectedbundle.so' -type f -print -quit 2>/dev/null)"
if [ -z "$helper" ] || [ -z "$bundle" ]; then
	echo "Error: WebKitGTK 6.0's WebKitWebProcess or injected bundle not found under /usr" >&2
	exit 1
fi
helper_dir="$(dirname "$helper")"
bundle_dir="$(dirname "$bundle")"
for f in "$helper_dir"/WebKit*Process; do
	install -D -m 755 "$f" "$APPDIR$f"
done
install -D -m 644 "$bundle" "$APPDIR$bundle"

echo "  Bundling libraries with linuxdeploy..."
cp "$GTK_PLUGIN" "$BUILD_DIR/linuxdeploy-plugin-gtk.sh"
chmod 755 "$BUILD_DIR/linuxdeploy-plugin-gtk.sh"
# linuxdeploy's own strip can't handle .relr.dyn sections, which modern
# toolchains emit; wails3 checks GTK for them the same way.
no_strip=""
for lib in /usr/lib/libgtk-4.so.1 /usr/lib64/libgtk-4.so.1 "/usr/lib/$ARCH-linux-gnu/libgtk-4.so.1"; do
	if [ -f "$lib" ] && readelf -S "$lib" | grep -q '\.relr\.dyn'; then
		no_strip=1
	fi
done
(cd "$BUILD_DIR" && PATH="$BUILD_DIR:$PATH" DEPLOY_GTK_VERSION=4 NO_STRIP="$no_strip" \
	quiet "$extracted/AppRun" --appdir "$APPDIR" --plugin gtk)

# Point the bundled WebKit at the bundled helpers (see the top of this file).
# Patched after linuxdeploy, which is what copies libwebkitgtk in.
mapfile -t libs < <(find "$APPDIR/usr/lib" -maxdepth 1 -type f -name 'libwebkitgtk-6.0.so*')
[ ${#libs[@]} -gt 0 ] || { echo "Error: linuxdeploy did not bundle libwebkitgtk-6.0" >&2; exit 1; }
for dir in "$helper_dir" "$bundle_dir"; do
	from="$dir"
	to="././/${dir#/usr/}"
	relocated=false
	for lib in "${libs[@]}"; do
		if grep -qaF "$from" "$lib"; then
			FROM="$from" TO="$to" perl -0777 -pi -e 's/\Q$ENV{FROM}\E/$ENV{TO}/g' "$lib"
			echo "  $(basename "$lib"): $from -> $to"
		fi
		# Checked after patching: the helper dir's rewrite also covers the
		# injected bundle's when one holds the other.
		if grep -qaF "$to" "$lib"; then
			relocated=true
		fi
	done
	# A WebKit that stores the directory some other way would leave the helpers
	# unreachable again; fail the release rather than ship that.
	if [ "$relocated" != true ]; then
		echo "Error: the bundled libwebkitgtk-6.0 does not reference $from" >&2
		exit 1
	fi
done

# Pack with linuxdeploy's AppImage plugin directly: going through linuxdeploy
# again would redeploy the libraries, the patched one included.
echo "  Packing $(basename "$OUTPUT")..."
mkdir -p "$(dirname "$OUTPUT")"
rm -f "$OUTPUT"
(cd "$(dirname "$OUTPUT")" && LDAI_OUTPUT="$(basename "$OUTPUT")" \
	quiet "$extracted/usr/bin/linuxdeploy-plugin-appimage" --appdir "$APPDIR")
[ -f "$OUTPUT" ] || { echo "Error: packing did not produce $OUTPUT" >&2; exit 1; }
