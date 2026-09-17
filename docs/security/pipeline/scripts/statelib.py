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
import re
import sys
import tempfile
from datetime import datetime, timezone

# Suppression statuses and the auto-demotion threshold live here so the readers
# (report.py, backfill_issues.py) share ONE filter, not a copy each — the copies
# are exactly where the CRITICAL/HIGH carve-out and the review_after check went
# missing from one site.
RESOLVED_STATUSES = {"false-positive", "accepted-risk", "fixed"}
UNFIXED_AGE_THRESHOLD_DAYS = 365


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
            # ensure_ascii=False: the triage reasons contain non-ASCII punctuation.
            # Escaping it to \uXXXX makes the first write after a hand edit churn
            # hundreds of lines in an auto-committed state file; keeping it literal
            # round-trips the file unchanged.
            json.dump(data, f, indent=2, ensure_ascii=False)
        os.replace(tmp, path)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise


def cve_age_days(cve_id):
    """Estimate CVE age from its ID (CVE-YYYY-NNNNN). Approximate: assumes the CVE
    was published on 1 January of its year (permissive by up to a year)."""
    match = re.match(r"CVE-(\d{4})-", cve_id)
    if not match:
        return 0
    year = int(match.group(1))
    age = (datetime.now(timezone.utc) - datetime(year, 1, 1, tzinfo=timezone.utc)).days
    return max(age, 0)


def review_after_passed(entry):
    """True if the override carries a review_after date that is now due/past."""
    raw = entry.get("review_after")
    if not raw:
        return False
    try:
        due = datetime.fromisoformat(str(raw).replace("Z", "+00:00"))
    except ValueError:
        # review_after is hand-edited by maintainers, so "2026-13-01" is ordinary
        # input. Fail toward review: surface the finding rather than letting a
        # typo turn a time-boxed acceptance into a silent permanent one.
        print(f"WARNING: unparsable review_after {raw!r} on a triage override — "
              f"treating as due so the finding surfaces for review", file=sys.stderr)
        return True
    if due.tzinfo is None:
        due = due.replace(tzinfo=timezone.utc)
    return datetime.now(timezone.utc) >= due


def override_suppresses(cve_id, triage_overrides):
    """A resolved override suppresses the finding — UNTIL its review_after passes."""
    entry = triage_overrides.get(cve_id)
    if not entry:
        return False
    return entry.get("status") in RESOLVED_STATUSES and not review_after_passed(entry)


def override_review_due(cve_id, triage_overrides):
    """A resolved override whose review_after has passed — due to re-surface."""
    entry = triage_overrides.get(cve_id)
    if not entry:
        return False
    return entry.get("status") in RESOLVED_STATUSES and review_after_passed(entry)


def age_drops(record, cve_id):
    """Unfixed, older than the threshold, and low enough severity to auto-demote.
    CRITICAL and HIGH are exempt: an unfixed one is what a human must decide on,
    not something to drop by age."""
    if record.get("fixed_version"):
        return False
    severity = record.get("severity", "")
    return cve_age_days(cve_id) > UNFIXED_AGE_THRESHOLD_DAYS and severity not in ("CRITICAL", "HIGH")
