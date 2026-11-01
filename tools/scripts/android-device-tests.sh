#!/usr/bin/env bash
# =============================================================================
# The app's instrumented tests (app/src/androidTest) on an Android emulator on
# a GitHub-hosted Linux runner, with nothing but the runner's own Android SDK:
# sdkmanager, avdmanager, the emulator and adb. No third-party action.
#
# Until this ran, every device result in docs/phase19 was a local emulator run
# (production-readiness F6). An emulator is not a phone: carrier behaviour,
# OEM changes and real Keystore hardware still need X3.
#
# Usage: android-device-tests.sh            (from the repository root)
#   ANDROID_TEST_API   system image API level (default 35)
# =============================================================================
set -euo pipefail

api="${ANDROID_TEST_API:-35}"
[[ "${api}" =~ ^[0-9]{2}$ ]] || { echo "::error::ANDROID_TEST_API must be a two-digit API level, got '${api}'"; exit 1; }
image="system-images;android-${api};google_apis;x86_64"
sdk="${ANDROID_HOME:?ANDROID_HOME is not set}"
bin="${sdk}/cmdline-tools/latest/bin"
adb="${sdk}/platform-tools/adb"
avd="aegis-ci-${api}"
# One AVD directory for avdmanager and the emulator: recent command-line tools
# default to an XDG directory the emulator does not search.
export ANDROID_AVD_HOME="${ANDROID_AVD_HOME:-${HOME}/.android/avd}"
mkdir -p "${ANDROID_AVD_HOME}"
# Diagnostics the workflow keeps: the emulator's log and logcat. Test data
# only; the app under test holds no real session or account.
out="${DEVICE_TEST_LOGS:-${PWD}/android/app/build/device-test-logs}"
mkdir -p "${out}"

test -e /dev/kvm || { echo "::error::/dev/kvm is missing: the emulator would be too slow to run the tests"; exit 1; }

echo "::group::system image ${image}"
yes | "${bin}/sdkmanager" --licenses >/dev/null || true
"${bin}/sdkmanager" --install "${image}" "emulator" "platform-tools" >/dev/null
echo no | "${bin}/avdmanager" create avd --force --name "${avd}" --package "${image}"
test -f "${ANDROID_AVD_HOME}/${avd}.ini" || { echo "::error::avdmanager did not create ${avd} in ${ANDROID_AVD_HOME}"; exit 1; }
echo "::endgroup::"

log="${out}/emulator.log"
"${sdk}/emulator/emulator" -avd "${avd}" -no-window -no-audio -no-boot-anim -no-snapshot \
    -gpu swiftshader_indirect -camera-back none -camera-front none >"${log}" 2>&1 &
emulator_pid=$!
cleanup() {
    timeout 30 "${adb}" logcat -d >"${out}/logcat.txt" 2>&1 || true
    "${adb}" emu kill >/dev/null 2>&1 || true
    kill "${emulator_pid}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Boot, bounded: 15 minutes, and at once if the emulator exits. Either way
# the failure shows the emulator's own log. Unbounded, a dead emulator would
# hold the runner until the job timeout.
booted=""
for _ in $(seq 1 180); do
    if ! kill -0 "${emulator_pid}" 2>/dev/null; then
        echo "::error::the emulator exited before it finished booting"
        tail -50 "${log}"
        exit 1
    fi
    if [ "$("${adb}" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ]; then
        booted=yes
        break
    fi
    sleep 5
done
if [ -z "${booted}" ]; then
    echo "::error::the emulator did not finish booting in 15 minutes"
    tail -50 "${log}"
    exit 1
fi

# Animations off: UI tests wait on idleness, not on transitions.
for setting in window_animation_scale transition_animation_scale animator_duration_scale; do
    "${adb}" shell settings put global "${setting}" 0
done
"${adb}" shell input keyevent 82 || true
"${adb}" shell getprop ro.build.version.release | sed 's/^/Android /'

cd android
./gradlew --no-daemon --stacktrace :app:connectedDebugAndroidTest

# The minified build on the device. The instrumented tests above run the
# debug build; nothing had ever run the R8-minified one (staging: release's
# minification and resource shrinking, debug-signed), so a class R8 strips or
# renames that Hilt, serialization or reflection needs would first fail on a
# user's phone. This launches it with no configuration (sign-in is then
# unavailable, as in any build without endpoints) and requires: the process
# still alive after 15 s, no fatal exception in the crash buffer, and the
# app's window on screen. A smoke check, not a test of features.
echo "::group::minified staging build: launch smoke check"
./gradlew --no-daemon --stacktrace :app:assembleStaging
apk="app/build/outputs/apk/staging/app-staging.apk"
test -s "${apk}" || { echo "::error::${apk} was not built"; exit 1; }
pkg="com.callscreen.staging"
"${adb}" install -r "${apk}"
# Clearing is best-effort: some emulator images refuse it ("failed to clear
# the 'main' log"). The crash check below counts only this package's crashes.
"${adb}" logcat -c || true
"${adb}" logcat -b crash -c || true
started="$("${adb}" shell am start -W -n "${pkg}/com.callscreen.app.MainActivity" | tr -d '\r')"
echo "${started}"
grep -q "Status: ok" <<<"${started}" || { echo "::error::the minified app did not start"; exit 1; }
for _ in $(seq 1 15); do
    sleep 1
    if [ -z "$("${adb}" shell pidof "${pkg}" | tr -d '\r')" ]; then
        "${adb}" logcat -d >"${out}/staging-smoke-logcat.txt" 2>&1 || true
        echo "::error::the minified app's process died within 15 s of launch"
        "${adb}" logcat -d -b crash | tail -40
        exit 1
    fi
done
"${adb}" logcat -d >"${out}/staging-smoke-logcat.txt" 2>&1 || true
crashes="$("${adb}" logcat -d -b crash | grep -c "Process: ${pkg}," || true)"
test "${crashes}" = "0" || { echo "::error::the minified app logged ${crashes} fatal exception(s)"; "${adb}" logcat -d -b crash | tail -40; exit 1; }
"${adb}" shell uiautomator dump /sdcard/staging-window.xml >/dev/null
"${adb}" shell cat /sdcard/staging-window.xml >"${out}/staging-window.xml"
grep -q "package=\"${pkg}\"" "${out}/staging-window.xml" || { echo "::error::the minified app's window is not on screen"; exit 1; }
echo "minified staging build: launched, alive after 15 s, no fatal exception, window on screen"
"${adb}" uninstall "${pkg}" >/dev/null || true
echo "::endgroup::"
