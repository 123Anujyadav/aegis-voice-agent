#!/usr/bin/env bash
# =============================================================================
# android-release-bundle.sh — build the release App Bundle signed with the
# upload key, and prove the signature. ONE code path for both callers:
#   * .github/workflows/release-android.yml, with the real upload key from the
#     protected GitHub Environment (secrets never leave the runner);
#   * pr-android, with a throwaway key generated for the job, so this path is
#     tested on every Android pull request long before a real key exists.
#
# Inputs (environment). The four upload-key values are SECRETS: they are never
# printed, and in GitHub Actions they are masked.
#   CS_VERSION_CODE, CS_VERSION_NAME   required here (a release always sets
#                                      them); validated by ReleaseVersion
#   UPLOAD_KEYSTORE_BASE64             the upload keystore (PKCS12 or JKS), base64
#   CS_UPLOAD_KEYSTORE_PASSWORD        its password
#   CS_UPLOAD_KEY_ALIAS                the key's alias (not sensitive)
#   CS_UPLOAD_KEY_PASSWORD             the key's password
#
# The public endpoint configuration (NOT secrets; the release workflow reads
# them from the play-release Environment's variables). A release must carry
# all four, or the build fails naming each one (build-logic ReleaseEndpoints):
# without them the app stops at sign-in and connects unpinned.
#   CS_IDENTITY_BASE_URL, CS_EDGE_API_BASE_URL   https origins
#   CS_IDENTITY_CERT_PINS, CS_EDGE_API_CERT_PINS  two or more sha256/ pins each
#   CS_PLAY_INTEGRITY_CLOUD_PROJECT_NUMBER        optional (unset: no integrity
#                                                 tokens; sessions are unverified)
#
# Output: app/build/outputs/bundle/release/app-release.aab (relative to
# android/), its SHA-256, and the signer certificate's SHA-256 fingerprint
# (public). The keystore is written only to a private temporary directory and
# deleted on exit, whatever happens.
#
# Usage (from the repository root): bash tools/scripts/android-release-bundle.sh
# =============================================================================
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"

missing=()
for name in CS_VERSION_CODE CS_VERSION_NAME UPLOAD_KEYSTORE_BASE64 CS_UPLOAD_KEYSTORE_PASSWORD CS_UPLOAD_KEY_ALIAS CS_UPLOAD_KEY_PASSWORD; do
  if [ -z "${!name:-}" ]; then missing+=("$name"); fi
done
if [ "${#missing[@]}" -gt 0 ]; then
  echo "release bundle: missing ${missing[*]} (names only; no value is printed)" >&2
  exit 1
fi
if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
  echo "::add-mask::$CS_UPLOAD_KEYSTORE_PASSWORD"
  echo "::add-mask::$CS_UPLOAD_KEY_PASSWORD"
fi

keydir="$(mktemp -d)"
chmod 700 "$keydir"
trap 'rm -rf "$keydir"' EXIT INT TERM
keystore="$keydir/upload.keystore"
if ! printf '%s' "$UPLOAD_KEYSTORE_BASE64" | base64 -d > "$keystore" 2>/dev/null || [ ! -s "$keystore" ]; then
  echo "release bundle: UPLOAD_KEYSTORE_BASE64 is not valid base64" >&2
  exit 1
fi
chmod 600 "$keystore"

# The certificate the bundle must be signed with, read from the keystore
# (public; printed below so the owner can register or check it in Play Console).
expected="$(keytool -exportcert -rfc -keystore "$keystore" -storepass:env CS_UPLOAD_KEYSTORE_PASSWORD -alias "$CS_UPLOAD_KEY_ALIAS" 2>/dev/null \
  | openssl x509 -noout -fingerprint -sha256 2>/dev/null)" || true
if [ -z "$expected" ]; then
  echo "release bundle: the keystore, its password or the alias is wrong (the keystore could not be read)" >&2
  exit 1
fi

# Passed as Gradle properties, each as one quoted argument; the build checks
# them (ReleaseEndpoints and the app's own patterns) before anything is built.
endpoint_args=(
  "-Pcallscreen.requireReleaseEndpoints=true"
  "-Pcallscreen.identityBaseUrl=${CS_IDENTITY_BASE_URL:-}"
  "-Pcallscreen.edgeApiBaseUrl=${CS_EDGE_API_BASE_URL:-}"
  "-Pcallscreen.identityCertPins=${CS_IDENTITY_CERT_PINS:-}"
  "-Pcallscreen.edgeApiCertPins=${CS_EDGE_API_CERT_PINS:-}"
  "-Pcallscreen.playIntegrityCloudProjectNumber=${CS_PLAY_INTEGRITY_CLOUD_PROJECT_NUMBER:-}"
)
echo "== building the release bundle (versionCode $CS_VERSION_CODE, versionName $CS_VERSION_NAME)"
(cd "$repo_root/android" && CS_UPLOAD_KEYSTORE_FILE="$keystore" ./gradlew --no-daemon --stacktrace "${endpoint_args[@]}" :app:bundleRelease)

bundle="$repo_root/android/app/build/outputs/bundle/release/app-release.aab"
if [ ! -s "$bundle" ]; then
  echo "release bundle: $bundle was not produced" >&2
  exit 1
fi
actual="$(keytool -printcert -rfc -jarfile "$bundle" 2>/dev/null | openssl x509 -noout -fingerprint -sha256 2>/dev/null)" || true
if [ -z "$actual" ] || [ "$expected" != "$actual" ]; then
  echo "release bundle: app-release.aab is not signed by the upload key" >&2
  exit 1
fi
echo "== upload key certificate: $expected"
echo "== bundle signer:          $actual"
echo "== bundle sha256:          $(sha256sum "$bundle" | cut -d' ' -f1)"
echo "== release bundle signed with the upload key"
