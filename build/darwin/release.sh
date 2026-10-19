#!/usr/bin/env bash
#
# release.sh - Package a macOS release: signed, notarized DMG + Sparkle appcast
#
# Run by `make release` on macOS, after the universal production build has
# put bin/brick-ui in place. Produces, in dist/:
#
#   brick-ui-<version>-macos-universal.dmg          the download, and the update
#   brick-ui-<version>-macos-universal.dmg.sha256   its checksum
#   appcast.xml                                     Sparkle's feed, one item
#
# The appcast carries the release notes Sparkle's update window shows, from
# build/darwin/release-notes/<version>.html (see the README.md there).
#
# The DMG serves both first installs (drag to Applications, on the
# background build/darwin/dmg/make-dmg.sh lays out) and Sparkle updates,
# which install from it directly. appcast.xml is published to
# https://webbite.io/desktop/macos/appcast.xml (SUFeedURL in Info.plist) once
# the DMG is up on the GitHub release it points at — see `make
# release-to-github`.
#
# Environment:
#   VERSION                release version: the git tag, minus any leading "v"
#   WAILS3                 the wails3 CLI (default: wails3 on PATH)
#   MAC_SIGN_IDENTITY      "Developer ID Application: …" certificate name
#   MAC_NOTARY_PROFILE     notarytool keychain profile (default: brick-notary),
#                          stored once with `xcrun notarytool store-credentials`
#   SPARKLE_PUBLIC_ED_KEY  stamped into Info.plist (by the bundle task)
#   SPARKLE_ED_KEY_FILE    optional: sign the update with this private key
#                          file instead of the one in the login keychain
#
# Flags:
#   --skip-notarize        local test runs only: sign and package, but don't
#                          submit to Apple. Allows MAC_SIGN_IDENTITY=- (ad hoc)
#                          and an untagged HEAD. `make release-to-github`
#                          refuses the result.

set -euo pipefail

GITHUB_REPO="webbite-io/brick-wails"
BUNDLE_NAME="Webbite Brick"
BIN_DIR="bin"
DIST_DIR="dist"
SPARKLE_BIN="build/darwin/sparkle/bin"
NOTES_DIR="build/darwin/release-notes"

WAILS3="${WAILS3:-wails3}"
MAC_NOTARY_PROFILE="${MAC_NOTARY_PROFILE:-brick-notary}"

skip_notarize=false
for arg in "$@"; do
  case "$arg" in
    --skip-notarize) skip_notarize=true ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

die() { echo "Error: $*" >&2; exit 1; }
step() { printf '\n\033[1m\033[34m%s\033[0m\n' "$*"; }

# --- Preconditions ---------------------------------------------------------

[ -n "${VERSION:-}" ] && [ "$VERSION" != "dev" ] ||
  die "no release version — tag the commit first (git tag 0.1.0)"
# Sparkle orders releases by version, so a release has to be an exact tag
# rather than something like 0.4.2-3-gabc1234 between two of them.
if [ "$(git describe --tags --exact-match 2>/dev/null | sed 's/^v//')" != "$VERSION" ]; then
  $skip_notarize || die "HEAD is not tagged $VERSION — macOS releases are built from an exact tag"
  echo "Warning: HEAD is not tagged $VERSION (allowed with --skip-notarize)" >&2
fi
[ -n "${MAC_SIGN_IDENTITY:-}" ] ||
  die "MAC_SIGN_IDENTITY is not set (the \"Developer ID Application: …\" certificate; see README.md)"
if [ "$MAC_SIGN_IDENTITY" = "-" ] && ! $skip_notarize; then
  die "an ad-hoc signature can't be notarized; set MAC_SIGN_IDENTITY to a Developer ID"
fi
[ -n "${SPARKLE_PUBLIC_ED_KEY:-}" ] ||
  die "SPARKLE_PUBLIC_ED_KEY is not set (see README.md, \"Sparkle keys\")"
# The notes Sparkle's update window shows, written into the appcast inline.
# Checked here, before anything is signed or sent to Apple.
notes_file="$NOTES_DIR/$VERSION.html"
if [ -f "$notes_file" ]; then
  grep -qF ']]>' "$notes_file" &&
    die "$notes_file contains \"]]>\", which would end the appcast's CDATA section early"
else
  $skip_notarize || die "$notes_file not found — write this release's notes first (see $NOTES_DIR/README.md)"
  echo "Warning: $notes_file not found; using placeholder notes (allowed with --skip-notarize)" >&2
fi
[ -f "$BIN_DIR/brick-ui" ] || die "$BIN_DIR/brick-ui not found — run the production build first"
lipo "$BIN_DIR/brick-ui" -verify_arch x86_64 arm64 ||
  die "$BIN_DIR/brick-ui is not a universal (x86_64 + arm64) binary"

app="$BIN_DIR/$BUNDLE_NAME.app"
dmg_name="brick-ui-$VERSION-macos-universal.dmg"
dmg="$DIST_DIR/$dmg_name"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# notarize submits a file to Apple's notary service and waits for the
# verdict, printing Apple's log when it isn't "Accepted" (notarytool exits 0
# for a rejected submission, so the status has to be read back).
notarize() {
  local file="$1" out="$work/notary.json" status id
  xcrun notarytool submit "$file" --keychain-profile "$MAC_NOTARY_PROFILE" \
    --wait --output-format json >"$out"
  status=$(plutil -extract status raw -o - "$out")
  id=$(plutil -extract id raw -o - "$out")
  if [ "$status" != "Accepted" ]; then
    xcrun notarytool log "$id" --keychain-profile "$MAC_NOTARY_PROFILE" >&2 || true
    die "notarization of $file came back \"$status\" (submission $id)"
  fi
}

# --- The app ---------------------------------------------------------------

step "Bundling $BUNDLE_NAME.app $VERSION..."
"$WAILS3" task darwin:create:app:bundle BRICK_VERSION="$VERSION"
plist="$app/Contents/Info.plist"
bundle_version=$(plutil -extract CFBundleVersion raw -o - "$plist")
[ "$bundle_version" = "$VERSION" ] ||
  die "the bundle's CFBundleVersion is \"$bundle_version\", not $VERSION"
plutil -extract SUPublicEDKey raw -o - "$plist" >/dev/null 2>&1 ||
  die "the bundle's Info.plist has no SUPublicEDKey"

step "Signing as $MAC_SIGN_IDENTITY..."
build/darwin/sign.sh "$app" "$MAC_SIGN_IDENTITY"

if ! $skip_notarize; then
  step "Notarizing the app..."
  ditto -c -k --keepParent "$app" "$work/app.zip"
  notarize "$work/app.zip"
  xcrun stapler staple "$app"
fi

# --- The DMG ---------------------------------------------------------------

step "Building $dmg_name..."
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"
build/darwin/dmg/make-dmg.sh "$app" "$dmg" "$BUNDLE_NAME"
if [ "$MAC_SIGN_IDENTITY" = "-" ]; then
  codesign --force --sign - "$dmg"
else
  codesign --force --sign "$MAC_SIGN_IDENTITY" --timestamp "$dmg"
fi

if ! $skip_notarize; then
  step "Notarizing the DMG..."
  notarize "$dmg"
  xcrun stapler staple "$dmg"
fi

# --- The appcast -----------------------------------------------------------

step "Signing the update for Sparkle..."
sign_args=()
if [ -n "${SPARKLE_ED_KEY_FILE:-}" ]; then
  sign_args=(--ed-key-file "$SPARKLE_ED_KEY_FILE")
fi
# Prints: sparkle:edSignature="…" length="…"
# (The ${a[@]+…} form: bash 3.2, macOS's own, treats an empty array as unset.)
enclosure_sig=$("$SPARKLE_BIN/sign_update" ${sign_args[@]+"${sign_args[@]}"} "$dmg")
case "$enclosure_sig" in
  *sparkle:edSignature=*length=*) ;;
  *) die "unexpected sign_update output: $enclosure_sig" ;;
esac

download_url="https://github.com/$GITHUB_REPO/releases/download/$VERSION/$dmg_name"
pub_date=$(LC_ALL=C date -u "+%a, %d %b %Y %H:%M:%S +0000")
min_macos=$(plutil -extract LSMinimumSystemVersion raw -o - "$plist" | sed -E 's/\.0$//')

# The release notes go in inline, as the item's <description>, rather than as
# a releaseNotesLink: Sparkle renders a linked page in a small webview with
# JavaScript off, which a GitHub release page doesn't survive. The style
# follows the system font and light/dark appearance.
if [ -f "$notes_file" ]; then
  notes=$(cat "$notes_file")
else
  notes="<p>Test build $VERSION.</p>"
fi

# One item, the release being made: Sparkle only needs the newest, and the
# full history lives on the GitHub releases page (fullReleaseNotesLink).
cat >"$DIST_DIR/appcast.xml" <<EOF
<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle">
  <channel>
    <title>Webbite Brick</title>
    <link>https://webbite.io/desktop/macos/appcast.xml</link>
    <item>
      <title>Version $VERSION</title>
      <pubDate>$pub_date</pubDate>
      <sparkle:version>$VERSION</sparkle:version>
      <sparkle:shortVersionString>$VERSION</sparkle:shortVersionString>
      <sparkle:minimumSystemVersion>$min_macos</sparkle:minimumSystemVersion>
      <description><![CDATA[
<style>
  :root { color-scheme: light dark; }
  body { font: 13px -apple-system, BlinkMacSystemFont, sans-serif; line-height: 1.45; margin: 8px 12px; }
  h1, h2, h3 { font-size: 13px; margin: 12px 0 4px; }
  ul { padding-left: 18px; margin: 4px 0; }
  li { margin: 2px 0; }
</style>
$notes
]]></description>
      <sparkle:fullReleaseNotesLink>https://github.com/$GITHUB_REPO/releases</sparkle:fullReleaseNotesLink>
      <enclosure url="$download_url" $enclosure_sig type="application/octet-stream"/>
    </item>
  </channel>
</rss>
EOF
plutil -lint "$plist" >/dev/null
xmllint --noout "$DIST_DIR/appcast.xml"

step "Generating the checksum..."
(cd "$DIST_DIR" && shasum -a 256 "$dmg_name" >"$dmg_name.sha256")

printf '\n\033[32m✓ macOS release %s packaged\033[0m\n' "$VERSION"
ls -lh "$DIST_DIR"
if $skip_notarize; then
  printf '\n\033[33mNot notarized (--skip-notarize): for local testing only.\033[0m\n'
fi
