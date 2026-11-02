#!/usr/bin/env bash
# =============================================================================
# android-apk-gate.sh — check a built APK against what was reviewed:
#   * its languages are exactly android/app/release-locales.txt. localeFilters
#     applies that list; this checks the result rather than the setting, so a
#     dependency or build change that adds, strips or renames a language fails
#     here instead of reaching a release;
#   * its size is within 2% of android/app/release-apk-size.txt (Phase 1 §17's
#     APK-size gate). A change that grows the APK on purpose updates that file
#     in the same pull request, where the growth is reviewed.
#
# Usage (from the repository root, after ./gradlew assembleStaging):
#   bash tools/scripts/android-apk-gate.sh android/app/build/outputs/apk/staging/app-staging.apk
# Needs ANDROID_HOME (for aapt2).
# =============================================================================
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
apk="${1:?usage: android-apk-gate.sh <apk>}"
locales_file="$repo_root/android/app/release-locales.txt"
size_file="$repo_root/android/app/release-apk-size.txt"
budget_percent=2

test -s "$apk" || { echo "::error::$apk is missing or empty"; exit 1; }
test -d "${ANDROID_HOME:-}/build-tools" || { echo "::error::ANDROID_HOME/build-tools not found"; exit 1; }
build_tools="$(ls -1 "$ANDROID_HOME/build-tools" | sort -V | tail -n 1)"
aapt2="$ANDROID_HOME/build-tools/$build_tools/aapt2"
[ -x "$aapt2" ] || [ -x "$aapt2.exe" ] || { echo "::error::aapt2 not found in build-tools $build_tools"; exit 1; }
[ -x "$aapt2" ] || aapt2="$aapt2.exe"

# --- Languages ---------------------------------------------------------------
# A resource configuration starts with optional mcc/mnc, then the locale:
# "hi", "en-rGB", "b+sr+Latn", or none at all ("night-v8", "v21", "ldrtl").
# A bare language is two letters, or three for the few with no two-letter code;
# the only other two- or three-letter leading qualifiers are UI modes ("car").
expected="$(sed -e 's/#.*//' -e 's/[[:space:]]//g' "$locales_file" | grep -v '^$' | sort -u)"
actual="$("$aapt2" dump configurations "$apk" | tr -d '\r' | awk -F- '
  {
    i = 1
    while ($i ~ /^(mcc|mnc)[0-9]+$/) i++
    q = $i
    if (q ~ /^b\+/) { split(q, parts, "+"); print parts[2] }
    else if (q ~ /^[a-z][a-z][a-z]?$/ && q != "car") print q
  }' | sort -u)"
# English is the default configuration (res/values, no qualifier), so it never
# appears as a qualifier of its own: count it as present.
actual="$(printf '%s\nen\n' "$actual" | grep -v '^$' | sort -u)"
if [ "$actual" != "$expected" ]; then
  echo "::error::the APK's languages differ from android/app/release-locales.txt"
  diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") || true
  exit 1
fi
echo "languages: $(printf '%s ' $actual)"

# --- Size --------------------------------------------------------------------
baseline="$(sed -e 's/#.*//' -e 's/[[:space:]]//g' "$size_file" | grep -v '^$' | head -n 1)"
[[ "$baseline" =~ ^[0-9]+$ ]] || { echo "::error::android/app/release-apk-size.txt holds no size in bytes"; exit 1; }
size="$(wc -c < "$apk" | tr -d '[:space:]')"
limit=$(( baseline + baseline * budget_percent / 100 ))
delta=$(( size - baseline ))
echo "size: $size bytes; reviewed: $baseline; change: $delta bytes; limit: $limit"
if [ "$size" -gt "$limit" ]; then
  echo "::error::the APK grew more than ${budget_percent}% over android/app/release-apk-size.txt ($baseline -> $size bytes). If the growth is intended, update that file in this pull request."
  exit 1
fi
