#!/usr/bin/env bash
#
# verify-release.sh - Check a macOS release DMG before it's published
#
# Usage: build/darwin/verify-release.sh <dmg> <version> <acc-api-url> <storage-api-url>
#
# Run by `make release-to-github` for each DMG in dist/; the macOS
# counterpart of its AppImage check. Fails unless the DMG holds a production
# build of <version> that Gatekeeper will open and Sparkle will accept:
#
#   - the binary has the production API URLs baked in
#   - CFBundleVersion is <version> (what Sparkle compares the appcast with)
#     and Info.plist carries an SUPublicEDKey
#   - the app's signature is valid, and Gatekeeper accepts it as notarized
#   - the DMG itself is signed with a stapled notarization ticket
#   - dist/appcast.xml offers this DMG as <version>

set -euo pipefail

dmg="${1:?usage: verify-release.sh <dmg> <version> <acc-api-url> <storage-api-url>}"
version="${2:?missing version}"
acc_api_url="${3:?missing ACC_API_URL}"
storage_api_url="${4:?missing STORAGE_API_URL}"

app_name="Webbite Brick.app"
appcast="$(dirname "$dmg")/appcast.xml"

fail() { echo "  ✗ $*" >&2; status=1; }
status=0

mnt=$(mktemp -d)
cleanup() {
  hdiutil detach -quiet "$mnt" 2>/dev/null || true
  rmdir "$mnt" 2>/dev/null || true
}
trap cleanup EXIT

if ! hdiutil attach -quiet -nobrowse -readonly -mountpoint "$mnt" "$dmg"; then
  echo "  ✗ could not mount $dmg" >&2
  exit 1
fi
app="$mnt/$app_name"
bin="$app/Contents/MacOS/brick-ui"
plist="$app/Contents/Info.plist"

if [ ! -f "$bin" ]; then
  echo "  ✗ $dmg has no $app_name/Contents/MacOS/brick-ui" >&2
  exit 1
fi

grep -aqF "$acc_api_url" "$bin" && grep -aqF "$storage_api_url" "$bin" ||
  fail "the binary doesn't have the production URLs ($acc_api_url, $storage_api_url) baked in"

bundle_version=$(plutil -extract CFBundleVersion raw -o - "$plist" 2>/dev/null || true)
[ "$bundle_version" = "$version" ] ||
  fail "CFBundleVersion is \"$bundle_version\", not $version"
plutil -extract SUPublicEDKey raw -o - "$plist" >/dev/null 2>&1 ||
  fail "Info.plist has no SUPublicEDKey, so Sparkle won't start"
[ -d "$app/Contents/Frameworks/Sparkle.framework" ] ||
  fail "Sparkle.framework is missing from the bundle"

codesign --verify --strict --deep "$app" 2>/dev/null ||
  fail "the app's code signature doesn't verify"
spctl --assess --type execute "$app" 2>/dev/null ||
  fail "Gatekeeper rejects the app (not Developer ID signed and notarized?)"
codesign --verify "$dmg" 2>/dev/null ||
  fail "the DMG isn't signed"
xcrun stapler validate -q "$dmg" 2>/dev/null ||
  fail "the DMG has no stapled notarization ticket"

if [ ! -f "$appcast" ]; then
  fail "$appcast is missing"
else
  grep -qF "<sparkle:version>$version</sparkle:version>" "$appcast" ||
    fail "$appcast doesn't offer version $version"
  grep -qF "/$(basename "$dmg")\"" "$appcast" ||
    fail "$appcast doesn't point at $(basename "$dmg")"
  grep -qF 'sparkle:edSignature=' "$appcast" ||
    fail "$appcast has no EdDSA signature for the update"
fi

exit $status
