#!/bin/sh
# Webbite Brick — user-level installer, shipped *inside* the release tarball.
#
# Installs the AppImage sitting next to this script to ~/.local/bin/brick-ui,
# drops the icon into the hicolor theme and writes a desktop entry so Brick
# shows up in the app launcher. Everything lands under $HOME — no root, no
# package manager.
#
#   ./install.sh              install (or upgrade in place)
#   ./install.sh --uninstall  remove everything this script installed
#   ./install.sh --help
#
# `make release` copies this file into brick-ui-<version>-linux-<arch>.tar.gz
# alongside the AppImage, so someone who downloads the tarball by hand gets a
# working installer. The repo-root install.sh downloads a release and then
# delegates here, which makes this script the single source of truth for what
# gets installed and where — the fetcher deliberately knows none of it.
#
# POSIX sh on purpose: it is the one script that runs on whatever the user's
# machine happens to have, before any of our own tooling is in place.

set -eu

APP_NAME="brick-ui"
DISPLAY_NAME="Webbite Brick"
COMMENT="Sync your files with Webbite Brick"

DEFAULT_BIN_DIR="${HOME}/.local/bin"
DATA_HOME="${XDG_DATA_HOME:-${HOME}/.local/share}"
ICON_ROOT="${DATA_HOME}/icons/hicolor"
DESKTOP_DIR="${DATA_HOME}/applications"
DESKTOP_FILE="${DESKTOP_DIR}/${APP_NAME}.desktop"
STATE_DIR="${DATA_HOME}/${APP_NAME}"

# Icon sizes generated when a resizer is available. The source PNG is 1024x1024;
# without a resizer we install it once at 256x256, which every desktop scales
# down fine — it's just a little wasteful.
ICON_SIZES="32 48 64 128 256 512"
FALLBACK_SIZE="256"

# Resolve the directory this script lives in, so it works when invoked from
# anywhere (./install.sh, sh /path/to/install.sh, etc).
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
SCRIPT_PATH="${SCRIPT_DIR}/$(basename -- "$0")"

COLOR_RESET=''
COLOR_BOLD=''
COLOR_GREEN=''
COLOR_YELLOW=''
if [ -t 1 ]; then
	COLOR_RESET='\033[0m'
	COLOR_BOLD='\033[1m'
	COLOR_GREEN='\033[32m'
	COLOR_YELLOW='\033[33m'
fi

say() { printf "%b\n" "$1"; }
# Warnings go to stdout, not stderr, so they stay interleaved in the right
# place when the caller pipes the output (stderr is unbuffered and would
# otherwise jump ahead of the block-buffered progress lines). Hard errors
# still go to stderr, where a caller checking for failure expects them.
warn() { printf "%b\n" "${COLOR_YELLOW}Warning:${COLOR_RESET} $1"; }
die() { printf "%b\n" "${COLOR_YELLOW}Error:${COLOR_RESET} $1" >&2; exit 1; }

usage() {
	cat <<EOF
${DISPLAY_NAME} - bundled installer

Usage: ./install.sh [options]

Options:
  --prefix PATH   Install the binary to PATH (default: ~/.local/bin)
  --uninstall     Remove the binary, icons and desktop entry
  --help          Show this help message

Icons, the desktop entry and the recorded state always go to XDG user
directories under \$HOME; --prefix only moves the binary.
EOF
}

BIN_DIR=""
UNINSTALL=false

while [ $# -gt 0 ]; do
	case "$1" in
	--prefix)
		[ $# -ge 2 ] || die "--prefix requires a path"
		BIN_DIR="$2"
		shift 2
		;;
	--prefix=*)
		BIN_DIR="${1#--prefix=}"
		shift
		;;
	--uninstall | -u)
		UNINSTALL=true
		shift
		;;
	--help | -h)
		usage
		exit 0
		;;
	*)
		die "unknown option '$1' (try --help)"
		;;
	esac
done

# The install dir is remembered at install time so --uninstall can find the
# binary again without being handed the same --prefix a second time.
if [ -z "${BIN_DIR}" ] && [ -f "${STATE_DIR}/bindir" ]; then
	BIN_DIR=$(cat "${STATE_DIR}/bindir" 2>/dev/null || echo "")
fi
[ -n "${BIN_DIR}" ] || BIN_DIR="${DEFAULT_BIN_DIR}"
TARGET="${BIN_DIR}/${APP_NAME}"

# `make release` writes VERSION into the tarball. It is only used for display
# and bookkeeping: if it is missing (a bundle built before this existed), the
# install still succeeds and the fetcher simply won't be able to tell that this
# version is already installed.
read_bundled_version() {
	if [ -f "${SCRIPT_DIR}/VERSION" ]; then
		head -n1 "${SCRIPT_DIR}/VERSION" 2>/dev/null | tr -d ' \t\r\n'
	else
		echo ""
	fi
}

refresh_caches() {
	if command -v update-desktop-database >/dev/null 2>&1; then
		update-desktop-database "${DESKTOP_DIR}" >/dev/null 2>&1 || true
	fi
	if command -v gtk-update-icon-cache >/dev/null 2>&1; then
		gtk-update-icon-cache -f -t "${ICON_ROOT}" >/dev/null 2>&1 || true
	fi
}

install_icons() {
	source_icon="$1"
	# Prefer a real resize so each theme size is pixel-exact; fall back to
	# installing the full-size PNG under one size directory.
	resizer=''
	if command -v magick >/dev/null 2>&1; then
		resizer='magick'
	elif command -v convert >/dev/null 2>&1; then
		resizer='convert'
	fi

	if [ -n "${resizer}" ]; then
		for size in ${ICON_SIZES}; do
			dir="${ICON_ROOT}/${size}x${size}/apps"
			mkdir -p "${dir}"
			"${resizer}" "${source_icon}" -resize "${size}x${size}" \
				"${dir}/${APP_NAME}.png" 2>/dev/null ||
				cp "${source_icon}" "${dir}/${APP_NAME}.png"
		done
		say "  icons installed (${ICON_SIZES}) under ${ICON_ROOT}"
	else
		dir="${ICON_ROOT}/${FALLBACK_SIZE}x${FALLBACK_SIZE}/apps"
		mkdir -p "${dir}"
		cp "${source_icon}" "${dir}/${APP_NAME}.png"
		say "  icon installed at ${dir}/${APP_NAME}.png"
	fi
}

# Remove desktop entries that point at our binary but live under a different
# filename than the one we manage. AppImageLauncher and similar helpers register
# their own `appimagekit-*.desktop` on first launch, which would otherwise leave
# the user with two identical launcher tiles after an upgrade.
prune_duplicate_desktop_entries() {
	keep="$1"

	[ -d "${DESKTOP_DIR}" ] || return 0

	for entry in "${DESKTOP_DIR}"/*.desktop; do
		[ -e "${entry}" ] || continue
		[ "${entry}" = "${keep}" ] && continue

		# Match either the exact installed path or any AppImage named after us,
		# so entries left by a previous --prefix still get cleaned up.
		if grep -qE "^Exec=.*${APP_NAME}(\.AppImage)?( |$)" "${entry}" 2>/dev/null; then
			rm -f "${entry}"
			say "  removed duplicate desktop entry $(basename "${entry}")"
		fi
	done
}

write_desktop_entry() {
	version="$1"

	mkdir -p "${DESKTOP_DIR}"
	# Exec is the absolute installed path rather than a bare command name, so
	# launchers work even when ~/.local/bin isn't on the session PATH (common
	# for GUI sessions, which don't source your shell rc).
	# StartupWMClass matches the app id Wails sets, so the window groups under
	# this entry in docks and the alt-tab switcher instead of showing up as an
	# unnamed second icon.
	cat >"${DESKTOP_FILE}" <<EOF
[Desktop Entry]
Type=Application
Version=1.0
Name=${DISPLAY_NAME}
Comment=${COMMENT}
Exec=${TARGET}
Icon=${APP_NAME}
Terminal=false
Categories=Utility;Network;FileTransfer;
Keywords=sync;files;cloud;storage;brick;webbite;
StartupNotify=true
StartupWMClass=${APP_NAME}
X-AppImage-Version=${version}
EOF
	chmod 644 "${DESKTOP_FILE}"
	say "  desktop entry written to ${DESKTOP_FILE}"
}

# Record what we installed, and keep a copy of this installer next to it. The
# repo-root fetcher reads `version` to skip redundant downloads and execs the
# stashed copy for --uninstall, so it never has to carry its own duplicate of
# the paths above.
record_state() {
	version="$1"

	mkdir -p "${STATE_DIR}"
	printf '%s\n' "${BIN_DIR}" >"${STATE_DIR}/bindir"
	if [ -n "${version}" ]; then
		printf '%s\n' "${version}" >"${STATE_DIR}/version"
	else
		rm -f "${STATE_DIR}/version"
	fi
	if [ "${SCRIPT_PATH}" != "${STATE_DIR}/install.sh" ]; then
		cp "${SCRIPT_PATH}" "${STATE_DIR}/install.sh" 2>/dev/null || return 0
		chmod +x "${STATE_DIR}/install.sh" 2>/dev/null || true
	fi
}

check_fuse() {
	# AppImages mount themselves with FUSE 2. Distros increasingly ship only
	# FUSE 3, in which case the AppImage exits with a dlopen error for
	# libfuse.so.2. Detect it up front and point at the two ways out, rather
	# than letting the user hit a cryptic failure on first launch.
	if command -v ldconfig >/dev/null 2>&1; then
		if ldconfig -p 2>/dev/null | grep -q 'libfuse\.so\.2'; then
			return 0
		fi
	else
		# No ldconfig to ask — assume it's fine rather than warning wrongly.
		return 0
	fi
	warn "libfuse.so.2 was not found, which AppImages need in order to run."
	say "  Install it with one of:"
	say "    Debian/Ubuntu:  sudo apt install libfuse2"
	say "    Fedora:         sudo dnf install fuse-libs"
	say "    Arch:           sudo pacman -S fuse2"
	say "  Or run without FUSE: ${APP_NAME} --appimage-extract-and-run"
	say ""
}

check_path() {
	case ":${PATH}:" in
	*":${BIN_DIR}:"*) return 0 ;;
	esac
	say ""
	warn "${BIN_DIR} is not in your PATH, so the '${APP_NAME}' command won't resolve."
	say "  Add it for bash/zsh:  echo 'export PATH=\"${BIN_DIR}:\$PATH\"' >> ~/.profile"
	say "  Add it for fish:      fish_add_path ${BIN_DIR}"
	say "  (The applications-menu entry works regardless — it uses an absolute path.)"
}

uninstall() {
	say "${COLOR_BOLD}Removing ${DISPLAY_NAME}...${COLOR_RESET}"
	removed=0
	if [ -e "${TARGET}" ]; then
		rm -f "${TARGET}"
		say "  removed ${TARGET}"
		removed=1
	fi
	# Remove our own entry first, so the sweep below only ever reports entries
	# that really are strays from another installer or an older layout.
	if [ -e "${DESKTOP_FILE}" ]; then
		rm -f "${DESKTOP_FILE}"
		say "  removed ${DESKTOP_FILE}"
		removed=1
	fi
	prune_duplicate_desktop_entries ""
	for size in ${ICON_SIZES}; do
		icon="${ICON_ROOT}/${size}x${size}/apps/${APP_NAME}.png"
		if [ -e "${icon}" ]; then
			rm -f "${icon}"
			say "  removed ${icon}"
			removed=1
		fi
	done
	# Safe even when running as the stashed copy inside STATE_DIR: the shell
	# holds an open fd on this file, and unlinking leaves that inode readable
	# until the process exits.
	rm -rf "${STATE_DIR}"
	refresh_caches
	if [ "${removed}" -eq 0 ]; then
		say "Nothing to remove — ${DISPLAY_NAME} doesn't appear to be installed."
	else
		say "${COLOR_GREEN}✓ ${DISPLAY_NAME} removed${COLOR_RESET}"
		say ""
		say "Your settings and credentials in ~/.config/brick were left untouched."
		say "That directory is shared with the brick CLI, so removing it would sign"
		say "you out of both — delete it by hand only if you want a clean slate."
	fi
}

install_app() {
	appimage="${SCRIPT_DIR}/${APP_NAME}.AppImage"
	icon="${SCRIPT_DIR}/${APP_NAME}.png"

	[ -f "${appimage}" ] || die "${appimage} not found — run this script from inside the extracted release directory."
	[ -f "${icon}" ] || die "${icon} not found — run this script from inside the extracted release directory."

	version=$(read_bundled_version)

	if [ -n "${version}" ]; then
		say "${COLOR_BOLD}Installing ${DISPLAY_NAME} ${version}...${COLOR_RESET}"
	else
		say "${COLOR_BOLD}Installing ${DISPLAY_NAME}...${COLOR_RESET}"
	fi

	mkdir -p "${BIN_DIR}" || die "could not create ${BIN_DIR}"
	[ -w "${BIN_DIR}" ] || die "${BIN_DIR} is not writable"

	# Replacing a running AppImage in place would yank the mounted file out
	# from under it, so remove first — the kernel keeps the running copy alive
	# until it exits.
	rm -f "${TARGET}"
	cp "${appimage}" "${TARGET}"
	chmod 755 "${TARGET}"
	say "  installed ${TARGET}"

	install_icons "${icon}"
	# Prune before writing ours, so a stale entry sharing our filename can't be
	# deleted after we've just written it.
	prune_duplicate_desktop_entries "${DESKTOP_FILE}"
	write_desktop_entry "${version}"
	record_state "${version}"
	refresh_caches

	say ""
	say "${COLOR_GREEN}✓ ${DISPLAY_NAME} installed${COLOR_RESET}"
	say ""
	check_fuse
	say "Launch it from your applications menu, or run: ${APP_NAME}"
	check_path
}

if [ "${UNINSTALL}" = true ]; then
	uninstall
else
	install_app
fi
