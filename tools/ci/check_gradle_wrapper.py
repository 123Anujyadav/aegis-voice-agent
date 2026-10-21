"""Verify the committed Gradle wrapper against Gradle's published checksums.

``android/gradle/wrapper/gradle-wrapper.jar`` is a binary that every Android
build executes before anything else, and a pull request can replace it in a
diff nobody can read. This check proves, against the checksums Gradle itself
publishes for the version the wrapper declares, that:

1. ``distributionUrl`` is an https URL on services.gradle.org for a released
   ``gradle-<version>-bin.zip`` or ``-all.zip``;
2. ``distributionSha256Sum`` is present and equals Gradle's published sha256
   for that distribution, so the wrapper refuses any other download;
3. ``gradle-wrapper.jar`` hashes to Gradle's published wrapper-jar sha256 for
   that version.

It uses the standard library only. Any network or parse failure fails the
check: an unverifiable wrapper is treated as an unverified one.

    python3 -I tools/ci/check_gradle_wrapper.py [android directory]
"""

from __future__ import annotations

from collections.abc import Callable
import hashlib
from pathlib import Path
import re
import sys
import urllib.request

DISTRIBUTIONS = "https://services.gradle.org/distributions/"
URL = re.compile(
    r"""^https://services\.gradle\.org/distributions/gradle-(?P<version>\d+(?:\.\d+){1,2})-(?P<kind>bin|all)\.zip$"""
)
SHA256 = re.compile(r"""^[0-9a-f]{64}$""")
Fetch = Callable[[str], str]


def read_properties(path: Path) -> dict[str, str]:
    r"""Parse a Java .properties file, undoing the `\:` escapes Gradle writes."""
    props = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith(("#", "!")):
            continue
        key, sep, value = line.partition("=")
        if sep:
            props[key.strip()] = value.strip().replace("\\:", ":").replace("\\=", "=")
    return props


def fetch_https(url: str) -> str:
    """Return a published checksum file's text; raise on any failure."""
    if not url.startswith(DISTRIBUTIONS):
        msg = f"refusing to fetch outside {DISTRIBUTIONS}: {url}"
        raise ValueError(msg)
    with urllib.request.urlopen(url, timeout=30) as resp:  # noqa: S310 - https, fixed host (checked above)
        return resp.read().decode("ascii").strip()


def _published(fetch: Fetch, url: str) -> str:
    value = fetch(url).strip().lower()
    if not SHA256.match(value):
        msg = f"{url} did not return a sha256"
        raise ValueError(msg)
    return value


def check(android: Path, fetch: Fetch = fetch_https) -> list[str]:
    """List every way the wrapper fails verification; empty means verified."""
    wrapper = android / "gradle" / "wrapper"
    props_path, jar = wrapper / "gradle-wrapper.properties", wrapper / "gradle-wrapper.jar"
    if not props_path.is_file() or not jar.is_file():
        return [f"missing {props_path} or {jar}"]
    props = read_properties(props_path)
    url = props.get("distributionUrl", "")
    m = URL.match(url)
    if not m:
        return [
            f"distributionUrl is not a released Gradle distribution on services.gradle.org: {url!r}"
        ]
    version, kind = m["version"], m["kind"]
    problems = []
    pinned = props.get("distributionSha256Sum", "").lower()
    try:
        dist_sha = _published(fetch, f"{DISTRIBUTIONS}gradle-{version}-{kind}.zip.sha256")
        jar_sha = _published(fetch, f"{DISTRIBUTIONS}gradle-{version}-wrapper.jar.sha256")
    except (OSError, ValueError) as exc:
        return [f"could not read Gradle's published checksums for {version}: {exc}"]
    if not pinned:
        problems.append(
            "distributionSha256Sum is missing, so the wrapper would accept any download"
        )
    elif pinned != dist_sha:
        problems.append(
            f"distributionSha256Sum {pinned} is not Gradle's sha256 for gradle-{version}-{kind}.zip ({dist_sha})"
        )
    actual = hashlib.sha256(jar.read_bytes()).hexdigest()
    if actual != jar_sha:
        problems.append(
            f"gradle-wrapper.jar sha256 {actual} is not Gradle {version}'s published wrapper ({jar_sha})"
        )
    return problems


def main(argv: list[str]) -> int:
    """Verify the wrapper under argv[1] (default: android); 0 means verified."""
    android = Path(argv[1]) if len(argv) > 1 else Path("android")
    problems = check(android)
    for problem in problems:
        sys.stdout.write(f"::error::{problem}\n")
    if not problems:
        sys.stdout.write(
            f"{android}: Gradle wrapper jar and distribution match Gradle's published checksums\n"
        )
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
