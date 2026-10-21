"""Reject GitHub Actions references that are not pinned to a full commit SHA.

A tag such as ``actions/checkout@v4`` is mutable: whoever controls the action's
repository can move it, and the next workflow run executes whatever it points
at, with this repository's token and secrets in reach. A full 40-character
commit SHA cannot be moved. This check enforces that every ``uses:`` in every
workflow under ``.github/`` is one of:

- ``owner/repo[/path]@<40 hex>  # vX.Y.Z``: a third-party action or reusable
  workflow, pinned to a commit, with the release it corresponds to as a
  comment, so a reviewer and Renovate/Dependabot can still read the version;
- ``./path``: an action or reusable workflow in this repository, which is
  pinned by the commit being run;
- ``docker://image@sha256:<64 hex>``: a container action pinned by digest.

Anything else fails: a tag, a branch, a short or uppercase SHA, a SHA with no
version comment, or ``docker://`` without a digest. The check reads files only
(standard library, no network), and fails closed on a ``uses:`` it cannot parse.

    python3 -I tools/ci/check_action_pins.py [repository root]
"""

from __future__ import annotations

from pathlib import Path
import re
import sys

USES = re.compile(r"""^\s*(?:-\s+)?uses:\s*(?P<value>.*?)\s*$""")
USES_ANYWHERE = re.compile(r"""(?<![\w-])uses\s*:""")
PINNED = re.compile(
    r"""^(?P<ref>[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._/-]+)?)"""
    r"""@(?P<sha>[0-9a-f]{40})\s+#\s*(?P<version>v\d+(?:\.\d+)*)\b.*$"""
)
LOCAL = re.compile(r"""^\./[A-Za-z0-9._/-]+$""")
DOCKER = re.compile(r"""^docker://[^\s@]+@sha256:[0-9a-f]{64}$""")
QUOTED = re.compile(r"""^(['"])(?P<inner>.*)\1$""")
FULL_SHA = re.compile(r"""[0-9a-fA-F]{40}""")


def _unquote(value: str) -> str:
    """Strip one pair of matching quotes around the reference, keeping any comment."""
    head, sep, comment = value.partition("#")
    ref = head.strip()
    m = QUOTED.match(ref)
    if m:
        ref = m["inner"]
    return f"{ref} {sep}{comment}".strip() if sep else ref


def _reason(ref: str) -> str:
    """Explain why a reference that matched no accepted form is rejected."""
    if not ref:
        return "empty uses:"
    if ref.startswith("docker://"):
        return "docker:// action not pinned by sha256 digest"
    if "@" not in ref:
        return "no @ref: unpinned action"
    pin = ref.rsplit("@", 1)[1]
    if FULL_SHA.fullmatch(pin):
        if pin != pin.lower():
            return "commit SHA must be lowercase"
        return "pinned SHA has no '# vX.Y.Z' version comment"
    return f"'@{pin}' is a tag or branch, not a full 40-character commit SHA"


def check_line(value: str) -> str | None:
    """Return why one ``uses:`` value is not acceptable, or None if it is."""
    value = _unquote(value)
    ref = value.split("#", 1)[0].strip()
    if LOCAL.match(ref) or DOCKER.match(ref) or PINNED.match(value):
        return None
    return _reason(ref)


def check_file(path: Path) -> list[str]:
    """List every violation in one workflow or action file as 'path:line: reason'."""
    problems = []
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        if line.lstrip().startswith("#"):
            continue
        m = USES.match(line)
        if not m:
            # A `uses:` in a form this check does not read (for example a flow
            # mapping) fails rather than being skipped unchecked.
            if USES_ANYWHERE.search(line.split("#", 1)[0]):
                problems.append(f"{path.as_posix()}:{number}: unparseable uses: {line.strip()}")
            continue
        reason = check_line(m["value"])
        if reason:
            problems.append(f"{path.as_posix()}:{number}: {reason}: {m['value']}")
    return problems


def workflow_files(root: Path) -> list[Path]:
    """Every workflow and action definition under .github/, sorted."""
    github = root / ".github"
    found = {
        *github.glob("workflows/*.yml"),
        *github.glob("workflows/*.yaml"),
        *github.rglob("action.yml"),
        *github.rglob("action.yaml"),
    }
    return sorted(found)


def main(argv: list[str]) -> int:
    """Check the repository at argv[1] (default: the current directory); 0 means all pinned."""
    root = Path(argv[1]) if len(argv) > 1 else Path()
    files = workflow_files(root)
    if not files:
        sys.stdout.write(
            f"::error::no workflow files under {root / '.github'}; refusing to pass vacuously\n"
        )
        return 1
    problems = [p for f in files for p in check_file(f)]
    for problem in problems:
        sys.stdout.write(f"::error::{problem}\n")
    sys.stdout.write(f"checked {len(files)} file(s): {len(problems)} unpinned reference(s)\n")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
