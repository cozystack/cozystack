#!/usr/bin/env python3
"""Estimate the size of the Secret in which Helm stores a release of a chart.

Helm's Secret storage driver keeps each revision as one Secret whose `release`
value is base64(gzip(json(release))), and the apiserver refuses a Secret whose
data exceeds 1 MiB. A chart that crosses it installs fine at first and then
fails the one upgrade whose rendered manifest is large enough, with "Secret
sh.helm.release.v1.<name>.vN is invalid: data: Too long". The stored release
carries the whole chart, not just what renders: every file under the chart dir
that is not Chart.yaml, values.yaml, values.schema.json, templates/ or charts/
ends up in chart.files, and the production path (the packages OCI artifact
loaded by helm-controller) applies no .helmignore, so tests/, images/ and the
Makefile are stored too.

This reproduces the encoding: the release JSON with the chart's templates and
files as base64 []byte, the rendered manifest read from stdin (`helm template`
output), gzip at Helm's BestCompression, then base64. It does not reproduce
Helm's gzip byte for byte or the runtime fields (info, config); measured
against real release Secrets it lands within about 0.5 percent, which is why
callers compare it to a threshold with headroom rather than to the limit.

Usage: helm template ... | helm-release-size.py <chart-dir> [--exclude GLOB ...]
Prints the estimated stored size in bytes.
"""
import base64
import fnmatch
import gzip
import json
import os
import sys

# Paths Helm keeps out of chart.files: they are stored elsewhere in the chart
# object (metadata, values, schema, templates) or are subcharts.
NOT_FILES = {"Chart.yaml", "Chart.lock", "values.yaml", "values.schema.json"}
NOT_FILE_DIRS = ("templates/", "charts/")


def go_json(obj):
    # encoding/json escapes <, > and & inside strings; match it so the size
    # does not drift with how much HTML-ish text a chart carries.
    s = json.dumps(obj, separators=(",", ":"), ensure_ascii=False)
    return s.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")


def main(argv):
    if len(argv) < 2:
        sys.stderr.write(__doc__)
        return 2
    chart = argv[1].rstrip("/")
    excludes = []
    rest = argv[2:]
    while rest:
        if rest[0] != "--exclude" or len(rest) < 2:
            sys.stderr.write("unexpected argument: %s\n" % rest[0])
            return 2
        excludes.append(rest[1])
        rest = rest[2:]

    templates, files = [], []
    for root, dirs, names in os.walk(chart):
        dirs.sort()
        for n in sorted(names):
            path = os.path.join(root, n)
            rel = os.path.relpath(path, chart)
            if any(fnmatch.fnmatch(rel, g) for g in excludes):
                continue
            with open(path, "rb") as fh:
                data = base64.b64encode(fh.read()).decode()
            entry = {"name": rel, "data": data}
            if rel.startswith("templates/"):
                templates.append(entry)
            elif rel in NOT_FILES or rel.startswith(NOT_FILE_DIRS):
                continue
            else:
                files.append(entry)

    def read(rel):
        p = os.path.join(chart, rel)
        return open(p).read() if os.path.exists(p) else ""

    release = {
        "name": "release",
        "info": {"status": "deployed", "description": "Upgrade complete"},
        "chart": {
            "metadata": {"name": os.path.basename(chart), "version": "0.0.0", "apiVersion": "v2"},
            "lock": None,
            "templates": templates,
            # Helm stores the parsed values and the user config; raw text with
            # comments and no config errs high (about 6 KB on
            # backupstrategy-controller), the safe side for a budget.
            "values": read("values.yaml"),
            "schema": base64.b64encode(read("values.schema.json").encode()).decode() or None,
            "files": files,
        },
        "manifest": sys.stdin.read(),
        "version": 1,
        "namespace": "default",
    }
    raw = go_json(release).encode()
    stored = base64.b64encode(gzip.compress(raw, compresslevel=9))
    print(len(stored))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
