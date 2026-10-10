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


AGE_DROPPED = "age-dropped"


def filter_decision(cve_id, record, triage_overrides, dev_only=False):
    """Decide whether a finding is suppressed: (skip, reason).

    The one decision both readers make — report.classify_filter before reporting and
    backfill_issues.should_skip before a repair run files an issue. Each was a copy of
    the other, and every round a branch landed in one and not its mirror (the
    CRITICAL/HIGH age carve-out, the review-due override, the tracked-non-resolved
    case). Keeping the decision here means there is nothing to mirror: the callers
    only wrap it, and report.py adds its side effect (persisting an age-drop) when
    `reason` is AGE_DROPPED. Pure: it never mutates the triage entry.

    `dev_only` is supplied by the caller because only report.py sees the component
    paths that decide it.
    """
    if record.get("severity", "") == "UNKNOWN":
        return True, "unknown severity"

    entry = triage_overrides.get(cve_id)
    if entry and entry.get("status") in RESOLVED_STATUSES:
        # A resolved override suppresses until its review_after passes. Once due it
        # surfaces for re-review — unless it is dev/build-only, which an expired
        # suppression must not resurrect.
        if not review_after_passed(entry):
            return True, f"triaged as {entry['status']}"
        if dev_only:
            return True, "dev/build dependency only"
        return False, None

    if dev_only:
        return True, "dev/build dependency only"

    # Already tracked with a non-resolved status (surfaced for re-review, confirmed,
    # in progress): under active handling, so the age filter must not silently
    # re-suppress it without a review date.
    if entry:
        return False, None

    if age_drops(record, cve_id):
        return True, AGE_DROPPED
    return False, None


def now_ts():
    """Current time as a full-precision UTC ISO-8601 string. The single writer of
    timestamp fields (decided_at, closed_at), so those never drift into three
    different formats across the scripts."""
    return datetime.now(timezone.utc).isoformat()


def parse_ts(raw):
    """Parse an ISO-8601 timestamp (with a trailing Z or an offset, or a bare date)
    to an aware datetime, or None if absent/unparsable. Used to order events by real
    time rather than by day, so a same-day comparison is not a coin toss."""
    if not raw:
        return None
    try:
        dt = datetime.fromisoformat(str(raw).replace("Z", "+00:00"))
    except ValueError:
        return None
    return dt if dt.tzinfo else dt.replace(tzinfo=timezone.utc)


def surface_due_override(entry, issue_url=None):
    """Transition a resolved override whose review_after has elapsed back into the
    lifecycle so it surfaces once for a maintainer, recording the transition so it is
    not re-surfaced on every subsequent run.

    Mutates `entry` in place. report.py's classify_filter is the sole owner of this
    transition and calls it only after its own dev-only/severity filters, on a finding
    that will actually surface. backfill_issues.py, a repair tool, does not stamp — it
    creates the issue and leaves the lifecycle to report.py — so there is no second
    caller to keep in step.

    `new` is the model's lifecycle entry state (see SECURITY_FINDING_STATUS_MODEL.md):
    the entry leaves RESOLVED_STATUSES so it is no longer suppressed, and review_after
    is cleared (it has done its job). `decided_by` is left untouched — a maintainer's
    expired review is theirs, not the machine's; monthly.py separates the machine case
    by live status, not by rewriting this field. decided_at (record time) is bumped and
    closed_at (event time) is set to now, so this fresh re-review beats a stale closed
    issue in sync_issues.py, which orders decisions by closed_at.

    The record is also handed off from the issue whose decision it re-opens. That
    issue stays closed with its old label, and sync_issues.py lets a label change on
    the record's OWN issue past its staleness guard (re-labelling is how a maintainer
    revisits a decision). A stamped record that still named the old issue would have
    its old label read as a re-label on the next sync, reverting it to the expired
    decision and renewing the window, so the re-review would undo itself every cycle.
    With `issue_url` (the re-review issue just created) the record points at that
    issue, so closing it with a label is what syncs next; without one (MEDIUM/LOW,
    which get no issue) the issue reference is dropped and the old issue becomes an
    older, different issue the guard rejects."""
    entry["status"] = "new"
    entry["review_after"] = None
    entry["decided_at"] = now_ts()
    entry["closed_at"] = now_ts()
    number = issue_number_from_url(issue_url)
    if number is not None:
        entry["issue_number"] = number
        entry["issue_url"] = issue_url
    else:
        entry.pop("issue_number", None)
        entry.pop("issue_url", None)
    return entry


def issue_number_from_url(url):
    """The issue number at the end of a GitHub issue URL, or None."""
    match = re.search(r"/issues/(\d+)/?$", url or "")
    return int(match.group(1)) if match else None
