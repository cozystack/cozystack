#!/usr/bin/env python3
"""Reject added positional selectors on multi-resource Helm templates.

Usage: hack/check-helm-document-indices.py <base-ref>
Requires mikefarah/yq, like the chart test tooling. Existing indices are left
alone. This static check counts --- separated blocks with a top-level kind;
it does not evaluate conditional documents or resources emitted by helpers/ranges.
"""

import json
from pathlib import Path
import re
import subprocess
import sys


def git(*args):
    return subprocess.check_output(["git", *args], text=True)


def check(base):
    root = Path(git("rev-parse", "--show-toplevel").strip())
    paths = git("diff", "--name-only", "--diff-filter=ACMR", base, "--",
                "packages/*/*/tests/*_test.yaml").splitlines()
    errors = []
    for path in paths:
        added = set()
        for line in git("diff", "--no-ext-diff", "--unified=0", base, "--", path).splitlines():
            if line.startswith("@@"):
                number = int(re.search(r"\+(\d+)", line).group(1))
            elif line.startswith(("+++", "---", "diff ", "index ", "\\")):
                continue
            elif line.startswith("+"):
                if "documentIndex" in line:
                    added.add(number)
                number += 1
            elif line.startswith(" "):
                number += 1
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
                templates = test.get("templates", suite.get("templates", ["templates/*"]))
                for assertion in test.get("asserts", []):
                    index = assertion.get("documentIndex", test.get("documentIndex", {}))
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
        sys.exit("usage: check-helm-document-indices.py <base-ref>")
    sys.exit(check(sys.argv[1]))
