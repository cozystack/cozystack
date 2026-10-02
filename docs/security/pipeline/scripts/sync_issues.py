#!/usr/bin/env python3
"""
sync_issues.py — Sync closed GitHub Issues back to triage-overrides.json.

When a maintainer closes an issue with a triage label (security/confirmed,
security/false-positive, security/accepted-risk, security/fixed), this script
reads that decision and writes it into state/triage-overrides.json so the
next scan skips already-triaged CVEs.
"""

import json
import os
import re
import subprocess
from datetime import datetime, timedelta, timezone

from statelib import load_json, save_json, parse_ts, now_ts

REPO_ROOT = os.path.join(os.path.dirname(__file__), "..")
STATE_DIR = os.path.join(REPO_ROOT, "state")
TRIAGE_FILE = os.path.join(STATE_DIR, "triage-overrides.json")
ISSUES_REPO = os.environ.get("ISSUES_REPO", "cozystack/security-scanner")

# Map GitHub labels to triage statuses
LABEL_TO_STATUS = {
    "security/confirmed": "confirmed",
    "security/false-positive": "false-positive",
    "security/accepted-risk": "accepted-risk",
    "security/in-progress": "in-progress",
    "security/fixed": "fixed",
}


def extract_cve_from_title(title):
    """Extract CVE ID from issue title like 'security_critical: CVE-YYYY-NNNNN — ...'"""
    match = re.search(r"(CVE-\d{4}-\d+)", title)
    return match.group(1) if match else None


def get_closed_issues():
    """Fetch all closed issues with security triage labels."""
    # Get closed issues with any security label
    cmd = [
        "gh", "issue", "list",
        "--repo", ISSUES_REPO,
        "--state", "closed",
        "--label", "security/triage-needed",
        "--json", "number,title,labels,closedAt,author,assignees",
        "--limit", "500",
    ]
    # Also get issues with triage decision labels (not just triage-needed)
    all_issues = []
    for label in list(LABEL_TO_STATUS.keys()) + ["security/triage-needed"]:
        cmd = [
            "gh", "issue", "list",
            "--repo", ISSUES_REPO,
            "--state", "closed",
            "--label", label,
            "--json", "number,title,labels,closedAt,assignees",
            "--limit", "500",
        ]
        try:
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
            if result.returncode == 0:
                issues = json.loads(result.stdout)
                all_issues.extend(issues)
        except Exception as e:
            print(f"WARN: error fetching issues with label {label}: {e}")

    # Deduplicate by issue number
    seen = set()
    unique = []
    for issue in all_issues:
        if issue["number"] not in seen:
            seen.add(issue["number"])
            unique.append(issue)

    return unique


def determine_status(labels):
    """Determine triage status from issue labels. Most specific wins."""
    label_names = {l["name"] for l in labels}

    # Priority order: fixed > in-progress > confirmed > accepted-risk > false-positive
    for label, status in [
        ("security/fixed", "fixed"),
        ("security/in-progress", "in-progress"),
        ("security/confirmed", "confirmed"),
        ("security/accepted-risk", "accepted-risk"),
        ("security/false-positive", "false-positive"),
    ]:
        if label in label_names:
            return status

    return None


def main():
    triage = load_json(TRIAGE_FILE, {})
    issues = get_closed_issues()

    if not issues:
        print("No closed issues with security labels found.")
        return

    updated = 0
    for issue in issues:
        cve_id = extract_cve_from_title(issue["title"])
        if not cve_id:
            continue

        status = determine_status(issue["labels"])
        if not status:
            continue

        # Check if already in triage with same status
        existing = triage.get(cve_id, {})
        if existing.get("status") == status:
            continue

        # The staleness guard is only about a DIFFERENT, older issue reverting a newer
        # decision. Re-labelling the SAME closed issue (same number) is the latest word
        # on that very issue — the status differs (we passed the same-status check
        # above), so it must always apply; that is the documented way to revisit a
        # false-positive, and comparing its unchanged closed_at against itself would
        # wrongly skip it. A re-review stamp (statelib.surface_due_override) moves the
        # record to the new re-review issue, so the issue it re-opened counts as a
        # different, older issue here and its unchanged label cannot revert it.
        #
        # For a different issue: newest close wins, independent of API order — any issue
        # not strictly newer than what is on record is skipped, ties on close time break
        # by issue number, and an undated issue is not evidence of a newer decision than
        # a dated one on record. existing_ts falls back to decided_at because records
        # written before closed_at existed carry only decided_at; without the fallback
        # the guard would be dead for every such record and a stale issue would overwrite
        # unconditionally.
        existing_num = existing.get("issue_number")
        if issue["number"] != existing_num:
            issue_ts = parse_ts(issue.get("closedAt"))
            existing_ts = parse_ts(existing.get("closed_at") or existing.get("decided_at"))
            if existing_ts and not issue_ts:
                print(f"  SKIP {cve_id}: issue #{issue['number']} has no close time; "
                      f"keeping newer record (#{existing_num})")
                continue
            if issue_ts and existing_ts and (
                (issue_ts, issue["number"]) <= (existing_ts, existing_num if existing_num is not None else -1)
            ):
                print(f"  SKIP {cve_id}: issue #{issue['number']} not newer than "
                      f"recorded decision (#{existing_num})")
                continue

        assignees = [a["login"] for a in issue.get("assignees", [])]
        decided_by = assignees[0] if assignees else "unknown"

        entry = {
            "status": status,
            "reason": f"Triaged via issue #{issue['number']}",
            "decided_by": decided_by,
            # decided_at is the record time (when the pipeline learned of the decision)
            # — what the monthly report buckets on, kept monotonic. closed_at is the
            # decision's real event time, used only for the ordering/staleness compare
            # above so a late-recorded fix is not mis-bucketed into a past month.
            "decided_at": now_ts(),
            "closed_at": issue.get("closedAt") or now_ts(),
            "issue_number": issue["number"],
            "issue_url": f"https://github.com/{ISSUES_REPO}/issues/{issue['number']}",
        }
        # accepted-risk carries a 90-day re-review: without a review_after the
        # label-driven acceptance (the main triage channel) would be permanent, the
        # same gap report.py's age filter avoids. false-positive is terminal.
        if status == "accepted-risk":
            entry["review_after"] = (
                datetime.now(timezone.utc) + timedelta(days=90)
            ).date().isoformat()
        triage[cve_id] = entry
        updated += 1
        print(f"  {cve_id}: {status} (issue #{issue['number']}, by {decided_by})")

    if updated:
        save_json(TRIAGE_FILE, triage)
        print(f"\nUpdated {updated} triage entries in {TRIAGE_FILE}")
    else:
        print("No new triage decisions to sync.")


if __name__ == "__main__":
    main()
