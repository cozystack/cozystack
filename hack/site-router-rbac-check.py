#!/usr/bin/env python3
"""Compare the site-router-controller kubebuilder RBAC markers with the chart role.

Reads the rendered chart from stdin and the Go file carrying the markers as
argv[1]. Exits non-zero with a side-by-side report when the two disagree.

The markers are documentation with no generator behind them (see the comment
above the marker block), so "they agree" is only true for as long as something
checks it. Driven by hack/site-router-rbac.bats.
"""

import re
import sys

import yaml

MARKER = re.compile(r"^//\s*\+kubebuilder:rbac:(.*)$")


def parse_markers(path):
    """Every (group, resource, verbs, resourceNames) a marker line grants."""
    rules = set()
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            m = MARKER.match(line.strip())
            if not m:
                continue
            fields = {}
            for field in m.group(1).split(","):
                key, _, value = field.partition("=")
                fields[key.strip()] = value.strip().strip('"')
            groups = fields.get("groups", "").split(";")
            resources = fields.get("resources", "").split(";")
            verbs = frozenset(v for v in fields.get("verbs", "").split(";") if v)
            names = tuple(sorted(n for n in fields.get("resourceNames", "").split(";") if n))
            for group in groups:
                for resource in resources:
                    rules.add((group, resource, verbs, names))
    return rules


def parse_chart(stream):
    """The same tuples, read off every ClusterRole/Role the chart renders."""
    rules = set()
    for doc in yaml.safe_load_all(stream):
        if not doc or doc.get("kind") not in ("ClusterRole", "Role"):
            continue
        for rule in doc.get("rules", []) or []:
            verbs = frozenset(rule.get("verbs", []))
            names = tuple(sorted(rule.get("resourceNames", []) or []))
            for group in rule.get("apiGroups", []) or [""]:
                for resource in rule.get("resources", []) or []:
                    rules.add((group, resource, verbs, names))
    return rules


def show(rule):
    group, resource, verbs, names = rule
    text = "{}/{}: {}".format(group or '""', resource, ",".join(sorted(verbs)))
    if names:
        text += " (resourceNames: {})".format(",".join(names))
    return text


def main():
    if len(sys.argv) != 2:
        print("usage: site-router-rbac-check.py <reconciler.go> < <rendered chart>", file=sys.stderr)
        return 2

    markers = parse_markers(sys.argv[1])
    chart = parse_chart(sys.stdin)

    if not markers:
        print("no +kubebuilder:rbac markers found in {}".format(sys.argv[1]), file=sys.stderr)
        return 1
    if not chart:
        print("no ClusterRole/Role rules found in the rendered chart", file=sys.stderr)
        return 1
    if markers == chart:
        return 0

    print("the kubebuilder RBAC markers and the chart-authored role disagree.", file=sys.stderr)
    print("The chart is what the controller runs with; the markers are the", file=sys.stderr)
    print("documentation of record. Correct whichever one is wrong.", file=sys.stderr)
    for rule in sorted(markers - chart, key=show):
        print("  marker grants, chart does not:  {}".format(show(rule)), file=sys.stderr)
    for rule in sorted(chart - markers, key=show):
        print("  chart grants, marker does not:  {}".format(show(rule)), file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
