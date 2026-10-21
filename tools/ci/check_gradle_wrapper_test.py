"""Tests for check_gradle_wrapper (standard library only, no network)."""

from __future__ import annotations

import hashlib
from pathlib import Path
import shutil
import tempfile
import unittest

import check_gradle_wrapper as gw

JAR = b"a gradle wrapper jar"
JAR_SHA = hashlib.sha256(JAR).hexdigest()
DIST_SHA = "7a00d51fb93147819aab76024feece20b6b84e420694101f276be952e08bef03"
URL = "https\\://services.gradle.org/distributions/gradle-8.12-bin.zip"


def published(overrides: dict[str, str] | None = None) -> gw.Fetch:
    """A fake of Gradle's checksum files, keyed by URL suffix."""
    table = {
        "gradle-8.12-bin.zip.sha256": DIST_SHA,
        "gradle-8.12-wrapper.jar.sha256": JAR_SHA,
        **(overrides or {}),
    }

    def fetch(url: str) -> str:
        name = url.rsplit("/", 1)[1]
        if name not in table:
            raise OSError(f"404 {url}")
        return table[name]

    return fetch


class CheckTest(unittest.TestCase):
    """The wrapper directory against a fake of Gradle's published checksums."""

    def android(self, url: str = URL, sha: str | None = DIST_SHA, jar: bytes = JAR) -> Path:
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root, True)
        wrapper = root / "gradle" / "wrapper"
        wrapper.mkdir(parents=True)
        lines = [f"distributionUrl={url}"] + (
            [f"distributionSha256Sum={sha}"] if sha is not None else []
        )
        (wrapper / "gradle-wrapper.properties").write_text(
            "\n".join(["# comment", *lines, ""]), encoding="utf-8"
        )
        (wrapper / "gradle-wrapper.jar").write_bytes(jar)
        return root

    def test_the_official_wrapper_is_verified(self) -> None:
        self.assertEqual(gw.check(self.android(), published()), [])

    def test_a_replaced_wrapper_jar_fails(self) -> None:
        got = gw.check(self.android(jar=b"something else"), published())
        self.assertEqual(len(got), 1)
        self.assertIn("gradle-wrapper.jar sha256", got[0])

    def test_a_wrong_distribution_checksum_fails(self) -> None:
        got = gw.check(self.android(sha="0" * 64), published())
        self.assertEqual(len(got), 1)
        self.assertIn("distributionSha256Sum", got[0])

    def test_a_missing_distribution_checksum_fails(self) -> None:
        got = gw.check(self.android(sha=None), published())
        self.assertIn("missing", got[0])

    def test_a_distribution_from_elsewhere_fails(self) -> None:
        for url in (
            "https\\://example.com/gradle-8.12-bin.zip",
            "http\\://services.gradle.org/distributions/gradle-8.12-bin.zip",
            "https\\://services.gradle.org/distributions-snapshots/gradle-8.13-20250101-bin.zip",
        ):
            with self.subTest(url=url):
                got = gw.check(self.android(url=url), published())
                self.assertEqual(len(got), 1)
                self.assertIn("not a released Gradle distribution", got[0])

    def test_an_unreachable_checksum_fails_closed(self) -> None:
        def offline(_url: str) -> str:
            raise OSError("network unreachable")

        got = gw.check(self.android(), offline)
        self.assertIn("could not read Gradle's published checksums", got[0])

    def test_a_checksum_file_that_is_not_a_sha256_fails_closed(self) -> None:
        got = gw.check(
            self.android(), published({"gradle-8.12-wrapper.jar.sha256": "<html>moved</html>"})
        )
        self.assertIn("could not read", got[0])

    def test_missing_wrapper_files_fail(self) -> None:
        self.assertIn("missing", gw.check(Path(tempfile.mkdtemp()), published())[0])

    def test_fetch_refuses_other_hosts(self) -> None:
        with self.assertRaises(ValueError):
            gw.fetch_https("https://example.com/gradle-8.12-wrapper.jar.sha256")


class PropertiesTest(unittest.TestCase):
    """The .properties reader."""

    def test_escaped_colons_are_unescaped_and_comments_skipped(self) -> None:
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root, True)
        path = root / "p.properties"
        path.write_text("# x\n! y\n\nkey=https\\://h/a\\=b\n", encoding="utf-8")
        self.assertEqual(gw.read_properties(path), {"key": "https://h/a=b"})


if __name__ == "__main__":
    unittest.main()
