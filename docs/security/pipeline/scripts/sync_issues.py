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
from datetime import datetime, timezone

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


def load_json(path, default):
    try:
        with open(path) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return default


def save_json(path, data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(data, f, indent=2)


def extract_cve_from_title(title):
    """Extract CVE ID from issue title like 'security_critical: CVE-2025-15467 — ...'"""
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

        assignees = [a["login"] for a in issue.get("assignees", [])]
        decided_by = assignees[0] if assignees else "unknown"

        triage[cve_id] = {
            "status": status,
            "reason": f"Triaged via issue #{issue['number']}",
            "decided_by": decided_by,
            "decided_at": datetime.now(timezone.utc).strftime("%Y-%m-%d"),
            "issue_number": issue["number"],
            "issue_url": f"https://github.com/{ISSUES_REPO}/issues/{issue['number']}",
        }
        updated += 1
        print(f"  {cve_id}: {status} (issue #{issue['number']}, by {decided_by})")

    if updated:
        save_json(TRIAGE_FILE, triage)
        print(f"\nUpdated {updated} triage entries in {TRIAGE_FILE}")
    else:
        print("No new triage decisions to sync.")


if __name__ == "__main__":
    main()
