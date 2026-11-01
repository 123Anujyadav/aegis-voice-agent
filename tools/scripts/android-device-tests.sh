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
