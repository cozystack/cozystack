#!/usr/bin/env python3
"""Shared state I/O for the CVE-scanning pipeline.

The pipeline keeps its history in JSON files under state/ (reported-cves.json,
pending-weekly.json, triage-overrides.json). Every script that touches them must
read and write them the same crash-safe way, or one script's integrity guard is
quietly undone by another's bare `except: return {}` and in-place write.

These two functions are the single implementation the readers import — report.py,
monthly.py, backfill_issues.py and sync_issues.py — so there is no mirror to
forget when the guard changes.
"""
import json
import os
import tempfile


def load_json(path, default):
    """Load JSON, or return default only for a genuinely missing file.

    A missing file is a legitimate first run. A present-but-corrupt file must NOT
    be silently treated as empty and then overwritten: that erases the historic
    record (issue/advisory URLs, triage state), and a run killed mid-write leaves
    exactly such a truncated file. Fail loudly instead so it can be restored.
    """
    if not os.path.exists(path):
        return default
    try:
        with open(path) as f:
            return json.load(f)
    except json.JSONDecodeError as e:
        raise SystemExit(
            f"FATAL: {path} exists but is not valid JSON ({e}). Refusing to "
            f"continue and overwrite it with an empty default — restore it from "
            f"git history (it may be a truncated write) and re-run."
        )


def save_json(path, data):
    """Write JSON atomically.

    A run killed mid-dump must not leave a truncated file that the next run reads
    as empty. Write to a temp file in the same directory, then rename over the
    target (os.replace is atomic on POSIX).
    """
    directory = os.path.dirname(path) or "."
    os.makedirs(directory, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=directory, suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(data, f, indent=2)
        os.replace(tmp, path)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise
