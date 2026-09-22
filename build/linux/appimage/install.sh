#!/usr/bin/env bash
#
# install.sh - Install Webbite Brick (GUI) from GitHub releases
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/webbite-io/brick-wails/main/build/linux/appimage/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/webbite-io/brick-wails/main/build/linux/appimage/install.sh | bash -s -- --version 0.0.1
#   curl -fsSL https://raw.githubusercontent.com/webbite-io/brick-wails/main/build/linux/appimage/install.sh | bash -s -- --uninstall
#
# Downloads the AppImage release tarball, installs it to ~/.local/bin and
# registers a desktop entry + icons. Re-running upgrades in place: every
# artifact lands on a fixed path, so repeat runs overwrite rather than
# accumulate, and stale entries from older layouts are pruned.
#
# Linux only — the GUI is a GTK4/WebKitGTK app shipped as an AppImage.
#

set -euo pipefail

# Configuration
APP_NAME="brick-ui"
DISPLAY_NAME="Webbite Brick"
COMMENT="Tray companion for the Webbite Brick CLI"
GITHUB_REPO="webbite-io/brick-wails"
DEFAULT_INSTALL_DIR="$HOME/.local/bin"

# Desktop integration paths (XDG user dirs — no root required)
DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
ICON_ROOT="$DATA_HOME/icons/hicolor"
DESKTOP_DIR="$DATA_HOME/applications"
DESKTOP_FILE="$DESKTOP_DIR/${APP_NAME}.desktop"
STATE_DIR="$DATA_HOME/$APP_NAME"
VERSION_FILE="$STATE_DIR/version"

# Icon sizes written into the hicolor theme. The shipped PNG is 1024x1024; we
# only resize when a resizer is present, otherwise the full-size file is
# installed once at FALLBACK_SIZE and the desktop scales it down.
ICON_SIZES=(32 48 64 128 256 512)
FALLBACK_SIZE=256

# Parse command line arguments
VERSION=""
PREFIX=""
FORCE=false
UNINSTALL=false

while [[ $# -gt 0 ]]; do
  case $1 in
  --version)
    VERSION="$2"
    shift 2
    ;;
  --prefix)
    PREFIX="$2"
    shift 2
    ;;
  --force)
    FORCE=true
    shift
    ;;
  --uninstall)
    UNINSTALL=true
    shift
    ;;
  --help)
    cat <<EOF
$DISPLAY_NAME - Installation Script

Usage:
  install.sh [options]

Options:
  --version VERSION    Install specific version (e.g., 0.0.1)
  --prefix PATH        Install to PATH (default: ~/.local/bin)
  --force              Reinstall even if already at the target version
  --uninstall          Remove the app, icons and desktop entry
  --help               Show this help message

Examples:
  # Install (or upgrade to) the latest version
  ./install.sh

  # Install a specific version
  ./install.sh --version 0.0.1

  # One-line install from GitHub
  curl -fsSL https://raw.githubusercontent.com/$GITHUB_REPO/main/build/linux/appimage/install.sh | bash

  # Remove it again
  ./install.sh --uninstall

EOF
    exit 0
    ;;
  *)
    echo "Unknown option: $1"
    echo "Run with --help for usage information"
    exit 1
    ;;
  esac
done

# Colors (only if terminal supports it)
if [ -t 1 ]; then
  COLOR_RESET='\033[0m'
  COLOR_BOLD='\033[1m'
  COLOR_GREEN='\033[32m'
  COLOR_BLUE='\033[34m'
  COLOR_RED='\033[31m'
  COLOR_YELLOW='\033[33m'
else
  COLOR_RESET=''
  COLOR_BOLD=''
  COLOR_GREEN=''
  COLOR_BLUE=''
  COLOR_RED=''
  COLOR_YELLOW=''
fi

# Utility functions
info() {
  echo -e "\n${COLOR_BOLD}${COLOR_BLUE}==>${COLOR_RESET} ${COLOR_BOLD}$*${COLOR_RESET}"
}

success() {
  echo -e "${COLOR_GREEN}✓${COLOR_RESET} $*"
}

error() {
  echo -e "${COLOR_RED}✗ Error:${COLOR_RESET} $*" >&2
}

warning() {
  echo -e "${COLOR_YELLOW}⚠${COLOR_RESET} $*"
}

die() {
  error "$*"
  exit 1
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

# Cleanup function
TEMP_DIR=""
cleanup() {
  if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
    rm -rf "$TEMP_DIR"
  fi
}
trap cleanup EXIT INT TERM

check_prerequisites() {
  local missing=()

  command_exists curl || missing+=("curl")
  command_exists tar || missing+=("tar")

  if ! command_exists shasum && ! command_exists sha256sum; then
    missing+=("shasum or sha256sum")
  fi

  if [ ${#missing[@]} -gt 0 ]; then
    error "Missing required tools:"
    for tool in "${missing[@]}"; do
      echo "  - $tool"
    done
    exit 1
  fi
}

# The GUI is only released for Linux; a macOS/Windows user landing here should
# get a clear message rather than a 404 from the download step.
detect_os() {
  local os
  os="$(uname -s)"

  case "$os" in
  Linux*)
    echo "linux"
    ;;
  *)
    die "Unsupported operating system: $os ($DISPLAY_NAME is currently Linux-only)"
    ;;
  esac
}

detect_arch() {
  local arch
  arch="$(uname -m)"

  case "$arch" in
  x86_64)
    echo "amd64"
    ;;
  arm64 | aarch64)
    echo "arm64"
    ;;
  *)
    die "Unsupported architecture: $arch (supported: x86_64, arm64)"
    ;;
  esac
}

get_latest_version() {
  info "Fetching latest version from GitHub..." >&2

  local version
  version=$(curl -fsSL -H "User-Agent: brick-ui-installer" "https://api.github.com/repos/$GITHUB_REPO/releases/latest" |
    grep '"tag_name"' |
    sed -E 's/.*"tag_name": *"v?([^"]+)".*/\1/' || echo "")

  if [ -z "$version" ]; then
    die "Failed to fetch latest version from GitHub API"
  fi

  echo "$version"
}

determine_install_dir() {
  local install_dir

  if [ -n "$PREFIX" ]; then
    install_dir="$PREFIX"
  else
    install_dir="$DEFAULT_INSTALL_DIR"
  fi

  echo "$install_dir"
}

# Remove desktop entries that point at our binary but live under a different
# filename than the one we manage. AppImageLauncher and similar helpers register
# their own `appimagekit-*.desktop` on first launch, which would otherwise leave
# the user with two identical launcher tiles after an upgrade.
prune_duplicate_desktop_entries() {
  local keep="$1"
  local binary_path="$2"

  [ -d "$DESKTOP_DIR" ] || return 0

  local entry
  for entry in "$DESKTOP_DIR"/*.desktop; do
    [ -e "$entry" ] || continue
    [ "$entry" = "$keep" ] && continue

    # Match either the exact installed path or any AppImage named after us,
    # so entries left by a previous --prefix still get cleaned up.
    if grep -qE "^Exec=.*(${binary_path//\//\\/}|${APP_NAME}(\.AppImage)?)( |$)" "$entry" 2>/dev/null; then
      rm -f "$entry"
      success "Removed duplicate desktop entry $(basename "$entry")"
    fi
  done
}

refresh_desktop_caches() {
  if command_exists update-desktop-database; then
    update-desktop-database "$DESKTOP_DIR" >/dev/null 2>&1 || true
  fi
  if command_exists gtk-update-icon-cache; then
    gtk-update-icon-cache -f -t "$ICON_ROOT" >/dev/null 2>&1 || true
  fi
}

install_icons() {
  local source_icon="$1"

  local resizer=""
  if command_exists magick; then
    resizer="magick"
  elif command_exists convert; then
    resizer="convert"
  fi

  local size dir
  if [ -n "$resizer" ]; then
    for size in "${ICON_SIZES[@]}"; do
      dir="$ICON_ROOT/${size}x${size}/apps"
      mkdir -p "$dir"
      "$resizer" "$source_icon" -resize "${size}x${size}" "$dir/${APP_NAME}.png" 2>/dev/null ||
        cp "$source_icon" "$dir/${APP_NAME}.png"
    done
    success "Installed icons (${ICON_SIZES[*]}) to $ICON_ROOT"
  else
    dir="$ICON_ROOT/${FALLBACK_SIZE}x${FALLBACK_SIZE}/apps"
    mkdir -p "$dir"
    cp "$source_icon" "$dir/${APP_NAME}.png"
    success "Installed icon to $dir/${APP_NAME}.png"
  fi
}

write_desktop_entry() {
  local binary_path="$1"
  local version="$2"

  mkdir -p "$DESKTOP_DIR"

  # Exec uses the absolute installed path rather than a bare command name:
  # desktop sessions don't source your shell rc, so ~/.local/bin is frequently
  # absent from the launcher's PATH even when it's in your terminal's.
  # StartupWMClass matches the app id Wails sets, so the running window groups
  # under this entry instead of appearing as a second, unnamed icon.
  cat >"$DESKTOP_FILE" <<EOF
[Desktop Entry]
Type=Application
Version=1.0
Name=$DISPLAY_NAME
Comment=$COMMENT
Exec=$binary_path
Icon=$APP_NAME
Categories=Network;FileTransfer;
Keywords=brick;sync;backup;webbite;
Terminal=false
StartupNotify=true
StartupWMClass=$APP_NAME
X-AppImage-Version=$version
EOF
  chmod 644 "$DESKTOP_FILE"
  success "Registered desktop entry at $DESKTOP_FILE"
}

download_and_verify() {
  local url="$1"
  local archive_name="$2"
  local checksum_url="$3"

  if ! curl -fsSL --progress-bar "$url" -o "$archive_name"; then
    die "Failed to download $url

If this version exists, check https://github.com/$GITHUB_REPO/releases"
  fi
  success "Downloaded $archive_name"

  if ! curl -fsSL "$checksum_url" -o SHA256SUMS 2>/dev/null; then
    warning "No SHA256SUMS published for this release — skipping checksum verification"
    return 0
  fi

  local expected_checksum
  expected_checksum=$(grep "$archive_name" SHA256SUMS | awk '{print $1}')

  if [ -z "$expected_checksum" ]; then
    warning "$archive_name not listed in SHA256SUMS — skipping checksum verification"
    return 0
  fi

  local actual_checksum
  if command_exists sha256sum; then
    actual_checksum=$(sha256sum "$archive_name" | awk '{print $1}')
  else
    actual_checksum=$(shasum -a 256 "$archive_name" | awk '{print $1}')
  fi

  if [ "$expected_checksum" != "$actual_checksum" ]; then
    die "Checksum verification failed!
Expected: $expected_checksum
Actual:   $actual_checksum"
  fi
  success "Checksum verified"
}

extract_archive() {
  local archive_name="$1"

  tar -xzf "$archive_name"
  if [ ! -f "$APP_NAME/${APP_NAME}.AppImage" ]; then
    die "AppImage not found in archive"
  fi
  if [ ! -f "$APP_NAME/${APP_NAME}.png" ]; then
    die "Icon not found in archive"
  fi
}

install_appimage() {
  local install_dir="$1"
  local target="$install_dir/$APP_NAME"

  mkdir -p "$install_dir" || die "Could not create $install_dir"
  [ -w "$install_dir" ] || die "$install_dir is not writable"

  # Overwriting a running AppImage in place would pull the mounted image out
  # from under the running process, so unlink first — the kernel keeps the old
  # inode alive until that process exits.
  rm -f "$target"
  cp "$APP_NAME/${APP_NAME}.AppImage" "$target"
  chmod +x "$target"
  success "Installed $target"
}

record_version() {
  mkdir -p "$STATE_DIR"
  echo "$1" >"$VERSION_FILE"
}

installed_version() {
  [ -f "$VERSION_FILE" ] && cat "$VERSION_FILE" 2>/dev/null || echo ""
}

# AppImages mount themselves with FUSE 2. Distros increasingly ship only FUSE 3,
# where the AppImage aborts with a dlopen error for libfuse.so.2. Surface that
# up front instead of letting it fail on first launch.
check_fuse() {
  command_exists ldconfig || return 0
  ldconfig -p 2>/dev/null | grep -q 'libfuse\.so\.2' && return 0

  warning "libfuse.so.2 was not found — AppImages need it in order to run."
  echo "  Install it with one of:"
  echo "    Debian/Ubuntu:  sudo apt install libfuse2"
  echo "    Fedora:         sudo dnf install fuse-libs"
  echo "    Arch:           sudo pacman -S fuse2"
  echo "  Or run without FUSE: $APP_NAME --appimage-extract-and-run"
}

check_path() {
  local install_dir="$1"

  if echo ":${PATH}:" | grep -q ":${install_dir}:"; then
    return 0
  fi

  warning "$install_dir is not in your PATH, so the '$APP_NAME' command won't resolve."
  echo "  Add it for bash/zsh:  echo 'export PATH=\"$install_dir:\$PATH\"' >> ~/.profile"
  echo "  Add it for fish:      fish_add_path $install_dir"
  echo "  (The applications-menu entry works regardless — it uses an absolute path.)"
}

uninstall() {
  info "Removing $DISPLAY_NAME..."

  local install_dir
  install_dir=$(determine_install_dir)
  local target="$install_dir/$APP_NAME"
  local removed=0

  if [ -e "$target" ]; then
    rm -f "$target"
    success "Removed $target"
    removed=1
  fi

  # Remove our own entry first, so the sweep below only ever reports entries
  # that really are strays from another installer or an older layout.
  if [ -e "$DESKTOP_FILE" ]; then
    rm -f "$DESKTOP_FILE"
    success "Removed $DESKTOP_FILE"
    removed=1
  fi

  prune_duplicate_desktop_entries "" "$target"

  local size icon
  for size in "${ICON_SIZES[@]}"; do
    icon="$ICON_ROOT/${size}x${size}/apps/${APP_NAME}.png"
    if [ -e "$icon" ]; then
      rm -f "$icon"
      removed=1
    fi
  done
  if [ "$removed" -eq 1 ]; then
    success "Removed icons from $ICON_ROOT"
  fi

  rm -rf "$STATE_DIR"
  refresh_desktop_caches

  echo ""
  if [ "$removed" -eq 0 ]; then
    warning "Nothing to remove — $DISPLAY_NAME doesn't appear to be installed."
  else
    success "$DISPLAY_NAME removed"
    echo ""
    echo "Your settings and credentials in ~/.config/brick were left untouched."
    echo "That directory is shared with the brick CLI, so removing it would sign"
    echo "you out of both — delete it by hand only if you want a clean slate."
  fi
}

main() {
  if [ "$UNINSTALL" = true ]; then
    uninstall
    exit 0
  fi

  info "$DISPLAY_NAME - Installation Script"
  echo ""

  check_prerequisites

  local os arch
  os=$(detect_os)
  arch=$(detect_arch)
  echo "Detected platform: $os/$arch"

  if [ -z "$VERSION" ]; then
    VERSION=$(get_latest_version)
  fi

  local current
  current=$(installed_version)
  if [ -n "$current" ] && [ "$current" = "$VERSION" ] && [ "$FORCE" != true ]; then
    echo ""
    success "$DISPLAY_NAME $VERSION is already installed — nothing to do."
    echo "  Reinstall anyway with: --force"
    exit 0
  fi

  local install_dir
  install_dir=$(determine_install_dir)
  echo "Install directory: $install_dir"

  if [ -n "$current" ]; then
    info "Upgrading $current -> $VERSION"
  fi

  local archive_name="${APP_NAME}-${VERSION}-${os}-${arch}.tar.gz"
  local base_url="https://github.com/${GITHUB_REPO}/releases/download/${VERSION}"

  TEMP_DIR=$(mktemp -d)
  cd "$TEMP_DIR"

  info "Downloading from GitHub releases..."
  download_and_verify "$base_url/$archive_name" "$archive_name" "$base_url/SHA256SUMS"
  extract_archive "$archive_name"

  info "Installing $DISPLAY_NAME $VERSION:"
  install_appimage "$install_dir"
  install_icons "$APP_NAME/${APP_NAME}.png"
  # Prune before writing ours, so a stale entry sharing our filename can't be
  # deleted after we've just written it.
  prune_duplicate_desktop_entries "$DESKTOP_FILE" "$install_dir/$APP_NAME"
  write_desktop_entry "$install_dir/$APP_NAME" "$VERSION"
  record_version "$VERSION"
  refresh_desktop_caches

  echo ""
  success "Installation complete!"
  echo ""

  check_fuse
  check_path "$install_dir"

  echo ""
  echo "Launch $DISPLAY_NAME from your applications menu, or run:"
  echo "  $APP_NAME"
  echo ""
}

main
