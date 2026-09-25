#!/usr/bin/env bash
#
# install.sh - Install Webbite Brick (the GUI app) from GitHub releases
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/webbite-io/brick-wails/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/webbite-io/brick-wails/main/install.sh | bash -s -- --version 0.0.1
#   curl -fsSL https://raw.githubusercontent.com/webbite-io/brick-wails/main/install.sh | bash -s -- --uninstall
#
# This script only *fetches*: it detects the platform, resolves the release,
# downloads and verifies the tarball, then hands off to the install.sh bundled
# inside it (see build/linux/appimage/install.sh) which does the actual install.
# Keeping the two apart means the installer that runs is always the one shipped
# with the bundle it is installing, and this file never has to know where icons
# or desktop entries go.
#
# Linux only — the GUI is a GTK4/WebKitGTK app shipped as an AppImage.
#

set -euo pipefail

# Configuration
APP_NAME="brick-ui"
DISPLAY_NAME="Webbite Brick"
GITHUB_REPO="webbite-io/brick-wails"
DEFAULT_INSTALL_DIR="$HOME/.local/bin"

# State written by the bundled installer: the version that is installed, and a
# copy of the installer itself (which --uninstall delegates to).
DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
STATE_DIR="$DATA_HOME/$APP_NAME"
VERSION_FILE="$STATE_DIR/version"
BINDIR_FILE="$STATE_DIR/bindir"
STASHED_INSTALLER="$STATE_DIR/install.sh"

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
  --prefix PATH        Install the binary to PATH (default: ~/.local/bin)
  --force              Reinstall even if already at the target version
  --uninstall          Remove the app, icons and desktop entry
  --help               Show this help message

Examples:
  # Install (or upgrade to) the latest version
  ./install.sh

  # Install a specific version
  ./install.sh --version 0.0.1

  # One-line install from GitHub
  curl -fsSL https://raw.githubusercontent.com/$GITHUB_REPO/main/install.sh | bash

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

# An upgrade goes back where the last install put it: without this, someone who
# installed with --prefix and then re-ran the one-liner would get a second copy
# in ~/.local/bin and an orphaned first one.
determine_install_dir() {
  if [ -n "$PREFIX" ]; then
    echo "$PREFIX"
    return 0
  fi

  local recorded=""
  [ -f "$BINDIR_FILE" ] && recorded=$(cat "$BINDIR_FILE" 2>/dev/null || echo "")
  if [ -n "$recorded" ]; then
    echo "$recorded"
  else
    echo "$DEFAULT_INSTALL_DIR"
  fi
}

installed_version() {
  [ -f "$VERSION_FILE" ] && cat "$VERSION_FILE" 2>/dev/null || echo ""
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
  [ -f "$APP_NAME/${APP_NAME}.AppImage" ] || die "AppImage not found in archive"
  [ -f "$APP_NAME/${APP_NAME}.png" ] || die "Icon not found in archive"
}

# Hand off to the installer shipped inside the tarball. Older tarballs (cut
# before the installer was bundled) don't have one; say so plainly rather than
# reimplementing the install here and drifting out of sync with it.
run_bundled_installer() {
  local install_dir="$1"
  local bundled="$APP_NAME/install.sh"

  if [ ! -f "$bundled" ]; then
    die "This release does not bundle an installer.

Install it manually, or use a release that does:
  https://github.com/$GITHUB_REPO/releases"
  fi

  # `sh` explicitly: the tar may not preserve the executable bit on every
  # filesystem, and the bundled script is POSIX sh anyway.
  sh "$bundled" --prefix "$install_dir" || die "Bundled installer failed"
}

# --uninstall needs no download: the bundled installer stashed a copy of itself
# at install time, and it knows every path it created (including a non-default
# --prefix).
uninstall() {
  # No banner here — the stashed installer prints its own.
  if [ ! -f "$STASHED_INSTALLER" ]; then
    die "No installation found at $STASHED_INSTALLER.

If you installed $DISPLAY_NAME by hand, run install.sh --uninstall from the
extracted release directory instead."
  fi

  if [ -n "$PREFIX" ]; then
    sh "$STASHED_INSTALLER" --uninstall --prefix "$PREFIX"
  else
    sh "$STASHED_INSTALLER" --uninstall
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

  echo ""
  run_bundled_installer "$install_dir"
}

main
