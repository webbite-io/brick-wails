#!/usr/bin/env bash
#
# sign.sh - Code-sign the Webbite Brick .app bundle, inside out
#
# Usage:
#   build/darwin/sign.sh "bin/Webbite Brick.app"              # ad-hoc (local builds)
#   build/darwin/sign.sh "bin/Webbite Brick.app" "Developer ID Application: …"
#
# Signs the embedded Sparkle.framework's helpers first, then the framework,
# then the app, each with the hardened runtime — the order Sparkle documents
# for signing outside Xcode. Not `codesign --deep`: that would re-sign
# Sparkle's Downloader.xpc without the entitlements it ships with.
#
# With a real identity the signatures are timestamped, which notarization
# requires; ad-hoc signatures ("-") can't be.

set -euo pipefail

app="${1:?usage: sign.sh <app bundle> [identity]}"
identity="${2:--}"

opts=(--force --sign "$identity" --options runtime)
if [ "$identity" != "-" ]; then
  opts+=(--timestamp)
fi

sparkle="$app/Contents/Frameworks/Sparkle.framework"
if [ -d "$sparkle" ]; then
  codesign "${opts[@]}" "$sparkle/Versions/B/XPCServices/Installer.xpc"
  codesign "${opts[@]}" --preserve-metadata=entitlements "$sparkle/Versions/B/XPCServices/Downloader.xpc"
  codesign "${opts[@]}" "$sparkle/Versions/B/Autoupdate"
  codesign "${opts[@]}" "$sparkle/Versions/B/Updater.app"
  codesign "${opts[@]}" "$sparkle"
fi
codesign "${opts[@]}" "$app"
codesign --verify --strict --verbose=1 "$app"
