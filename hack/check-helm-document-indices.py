#!/usr/bin/env python3
"""Reject added positional selectors on multi-resource Helm templates.

Usage: python3 hack/check-helm-document-indices.py <base-ref>
Requires mikefarah/yq, like the chart test tooling.
"""

import json
from pathlib import Path
import re
import subprocess
import sys


def git(*args):
    return subprocess.check_output(["git", *args], text=True)


def check(base):
    """Check added indices, leaving existing ones alone.

    This static check counts --- separated blocks with a top-level kind; it does
    not evaluate conditional documents or resources emitted by helpers/ranges.
    """
    root = Path(git("rev-parse", "--show-toplevel").strip())
    additions = {}
    path = None
    for line in git("diff", "--no-ext-diff", "-M", "--unified=0", base, "--",
                    ":(glob)packages/*/*/tests/*_test.yaml").splitlines():
        if line.startswith("diff --git "):
            path = None
        elif line.startswith("+++ b/"):
            path = line[6:]
            additions[path] = set()
        elif line.startswith("@@"):
            number = int(re.search(r"\+(\d+)", line).group(1))
        elif path and line.startswith("+"):
            if "documentIndex" in line:
                additions[path].add(number)
            number += 1
        elif line.startswith(" "):
            number += 1
    errors = []
    for path, added in additions.items():
        if not added:
            continue

        # Preserve the key's source line while yq resolves multi-document YAML,
        # flow mappings and anchors; a text-only suite parser misses those.
        expression = ('[. | (.. | select(tag == "!!map" and has("documentIndex")) '
                      '| .documentIndex) |= {"line": key | line}]')
        suites = json.loads(subprocess.check_output(
            ["yq", "eval-all", "-o=json", expression, str(root / path)], text=True))
        chart = (root / path).parent.parent
        for suite in suites:
            if not suite:
                continue
            for test in suite.get("tests", []):
                templates = test.get("templates") or (
                    [test["template"]] if test.get("template")
                    else suite.get("templates") or ["templates/**/*"])
                for assertion in test.get("asserts", []):
                    index = test.get("documentIndex") or assertion.get("documentIndex", {})
                    number = index.get("line")
                    if number not in added:
                        continue
                    selected = [assertion["template"]] if "template" in assertion else templates
                    for pattern in selected:
                        matches = list(chart.glob(pattern))
                        if not matches:
                            matches = list((chart / "templates").glob(pattern))
                        for template in matches:
                            if not template.is_file():
                                continue
                            documents = re.split(r"(?m)^---[ \t]*(?:#.*)?$", template.read_text())
                            if sum(bool(re.search(r"(?m)^kind\s*:", doc)) for doc in documents) > 1:
                                errors.append(f"{path}:{number}: use documentSelector, not "
                                              f"documentIndex, for {template.relative_to(root)}")
    for error in sorted(set(errors)):
        print(error, file=sys.stderr)
    return bool(errors)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: python3 hack/check-helm-document-indices.py <base-ref>")
    sys.exit(check(sys.argv[1]))
