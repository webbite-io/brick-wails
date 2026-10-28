#!/usr/bin/env bash
#
# make-dmg.sh - Build the release DMG: the app and an Applications link, laid
# out on a background that says to drag one onto the other
#
# Usage: build/darwin/dmg/make-dmg.sh <app> <dmg> <volume-name>
#
# Run by release.sh. The window is laid out by dmgbuild (dmg-settings.py),
# installed on first run into build/darwin/dmg/.venv from the hash-pinned
# requirements.txt, so the first run needs network. The DMG is left
# unsigned; release.sh signs and notarizes it.

set -euo pipefail

app="${1:?usage: make-dmg.sh <app> <dmg> <volume-name>}"
dmg="${2:?missing dmg}"
volume_name="${3:?missing volume name}"

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
venv="$dir/.venv"

# Rebuild the venv whenever requirements.txt changes, so bumping a pin takes
# effect without anyone having to remember to delete it.
stamp="$venv/requirements.sha256"
want=$(shasum -a 256 "$dir/requirements.txt" | awk '{print $1}')
if [ "$(cat "$stamp" 2>/dev/null)" != "$want" ]; then
  rm -rf "$venv"
  python3 -m venv "$venv"
  "$venv/bin/pip" install --quiet --disable-pip-version-check --require-hashes \
    -r "$dir/requirements.txt"
  echo "$want" >"$stamp"
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# One TIFF holding both resolutions, so Finder picks the sharp one on a
# Retina display.
tiffutil -cathidpicheck "$dir/background.png" "$dir/background@2x.png" \
  -out "$work/background.tiff" >/dev/null 2>&1

rm -f "$dmg"
"$venv/bin/dmgbuild" -s "$dir/dmg-settings.py" \
  -D app="$app" \
  -D background="$work/background.tiff" \
  -D volume_icon="$dir/../icons.icns" \
  "$volume_name" "$dmg"
