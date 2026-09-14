#!/usr/bin/env python3
"""
backfill_issues.py — One-time script to create GitHub Issues for CVEs
that were reported before the Issue integration was added.

Reads state/reported-cves.json, finds entries without issue_url,
applies the same filters as report.py, and creates Issues.

Usage:
  python3 scripts/backfill_issues.py                    # dry run
  python3 scripts/backfill_issues.py --execute          # create issues
  python3 scripts/backfill_issues.py --execute --limit 50  # create max 50
"""

import argparse
import json
import os
import re
import subprocess
import time
from datetime import datetime, timezone

REPO_ROOT = os.path.join(os.path.dirname(__file__), "..")
STATE_DIR = os.path.join(REPO_ROOT, "state")
REPORTED_FILE = os.path.join(STATE_DIR, "reported-cves.json")
TRIAGE_FILE = os.path.join(STATE_DIR, "triage-overrides.json")
ISSUES_REPO = os.environ.get("ISSUES_REPO", "cozystack/security-scanner")

SEVERITY_LABELS = {
    "CRITICAL": "security/critical",
    "HIGH": "security/high",
    "MEDIUM": "security/medium",
    "LOW": "security/low",
}

UNFIXED_AGE_THRESHOLD_DAYS = 365


def load_json(path, default):
    try:
        with open(path) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return default


def save_json(path, data):
    with open(path, "w") as f:
        json.dump(data, f, indent=2)


def cve_age_days(cve_id):
    match = re.match(r"CVE-(\d{4})-", cve_id)
    if not match:
        return 0
    cve_year = int(match.group(1))
    now = datetime.now(timezone.utc)
    return max((now - datetime(cve_year, 1, 1, tzinfo=timezone.utc)).days, 0)


def should_skip(cve_id, data, triage):
    """Apply same filters as report.py."""
    # Already triaged
    if cve_id in triage:
        status = triage[cve_id].get("status", "")
        if status in ("false-positive", "accepted-risk", "fixed"):
            return True, f"triaged as {status}"

    # Unfixed > 365 days
    if not data.get("fixed_version"):
        age = cve_age_days(cve_id)
        if age > UNFIXED_AGE_THRESHOLD_DAYS:
            return True, f"unfixed {age}d"

    return False, None


def create_issue(cve_id, data):
    severity = data.get("severity", "UNKNOWN")
    package = data.get("package", "unknown")
    fixed = data.get("fixed_version", "")
    repos = data.get("repos", [])

    title = f"security_{severity.lower()}: {cve_id} in {package}"

    repos_md = "\n".join(f"- `{r}`" for r in repos) if repos else "- Unknown"
    fix_line = f"`{fixed}`" if fixed else "No fix available yet"
    report_file = data.get("report_file", "")
    report_link = f"\n**Report:** [{report_file}]({report_file})" if report_file else ""

    body = f"""## {cve_id}

| Field | Value |
|-------|-------|
| **CVE** | [{cve_id}](https://nvd.nist.gov/vuln/detail/{cve_id}) |
| **Severity** | {severity} |
| **Package** | `{package}` |
| **Fixed in** | {fix_line} |
{report_link}

### Affected Components

{repos_md}

### Triage Checklist

- [ ] Is this in a shipped artifact (not dev/test only)?
- [ ] Is the vulnerable code path reachable in our usage?
- [ ] Has the distro already backported the fix?
- [ ] Is a fix available upstream?
"""

    labels = ["security/triage-needed"]
    sev_label = SEVERITY_LABELS.get(severity)
    if sev_label:
        labels.append(sev_label)

    label_args = []
    for l in labels:
        label_args.extend(["-l", l])

    cmd = ["gh", "issue", "create",
           "--repo", ISSUES_REPO,
           "--title", title,
           "--body", body] + label_args

    result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
    if result.returncode == 0:
        return result.stdout.strip()
    else:
        print(f"    ERROR: {result.stderr[:200]}")
        return None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--execute", action="store_true", help="Actually create issues (default: dry run)")
    parser.add_argument("--limit", type=int, default=0, help="Max issues to create (0 = unlimited)")
    args = parser.parse_args()

    reported = load_json(REPORTED_FILE, {})
    triage = load_json(TRIAGE_FILE, {})

    # Find CVEs without issues
    needs_issue = {}
    skipped = 0
    for cve_id, data in reported.items():
        if data.get("issue_url"):
            continue
        skip, reason = should_skip(cve_id, data, triage)
        if skip:
            skipped += 1
            continue
        needs_issue[cve_id] = data

    # Sort: CRITICAL first, then HIGH, etc.
    sev_order = {"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3}
    sorted_cves = sorted(needs_issue.items(), key=lambda x: (sev_order.get(x[1].get("severity", ""), 9), x[0]))

    from collections import Counter
    sevs = Counter(d["severity"] for _, d in sorted_cves)

    print(f"Total reported CVEs: {len(reported)}")
    print(f"Already have issues: {sum(1 for d in reported.values() if d.get('issue_url'))}")
    print(f"Filtered out: {skipped}")
    print(f"Need issues: {len(sorted_cves)}")
    print(f"  By severity: {dict(sevs)}")
    print()

    if not args.execute:
        print("DRY RUN — pass --execute to create issues")
        print()
        for cve_id, data in sorted_cves[:20]:
            print(f"  {data['severity']:8s} {cve_id:25s} {data.get('package',''):20s} repos={len(data.get('repos',[]))}")
        if len(sorted_cves) > 20:
            print(f"  ... and {len(sorted_cves) - 20} more")
        return

    # Create issues
    limit = args.limit if args.limit > 0 else len(sorted_cves)
    created = 0

    for cve_id, data in sorted_cves[:limit]:
        print(f"  [{created+1}/{min(limit, len(sorted_cves))}] {data['severity']} {cve_id} ({data.get('package','')})")
        url = create_issue(cve_id, data)
        if url:
            reported[cve_id]["issue_url"] = url
            created += 1
            # Rate limit: GitHub allows 100 requests/min for authenticated users
            time.sleep(1)
        else:
            print(f"    Failed, stopping to avoid rate limit issues")
            break

    save_json(REPORTED_FILE, reported)
    print(f"\nCreated {created} issues. State saved.")


if __name__ == "__main__":
    main()
