#!/usr/bin/env python3
"""
report.py — Generate security reports as markdown files in reports/.

- CRITICAL: immediately saved to reports/critical/YYYY-MM-DD-<CVE>.md
- HIGH/MEDIUM/LOW: accumulated, then saved as weekly report
  reports/weekly/YYYY-MM-DD-security-report.md
"""

import argparse
import json
import os
import re
import subprocess
from datetime import datetime, timezone


REPO_ROOT = os.path.join(os.path.dirname(__file__), "..")
STATE_DIR = os.path.join(REPO_ROOT, "state")
REPORTS_DIR = os.path.join(REPO_ROOT, "reports")
REPORTED_FILE = os.path.join(STATE_DIR, "reported-cves.json")
PENDING_FILE = os.path.join(STATE_DIR, "pending-weekly.json")
TRIAGE_FILE = os.path.join(STATE_DIR, "triage-overrides.json")

# Statuses that mean "already handled, skip in new reports"
RESOLVED_STATUSES = {"false-positive", "accepted-risk", "fixed"}

# Build/dev image patterns — CVEs in these are informational only
DEV_IMAGE_PATTERNS = [
    r"^golang:",
    r"^docker\.io/library/golang:",
    r"^docker\.io/golang:",
    r"^node:",
    r"^python:",
    r"^rust:",
    r"^maven:",
    r"^gradle:",
    r"^--platform=",  # build stage artifacts
]

# Component paths that are dev/test only
DEV_COMPONENT_PATTERNS = [
    "core/testing",
    "hack/",
    "test/",
    "e2e/",
]

# How old an unfixed CVE must be (days) before we auto-demote it
UNFIXED_AGE_THRESHOLD_DAYS = 365

# Repository where issues are created (this repo)
ISSUES_REPO = os.environ.get("ISSUES_REPO", "cozystack/security-scanner")

# Severity → GitHub label mapping
SEVERITY_LABELS = {
    "CRITICAL": "security/critical",
    "HIGH": "security/high",
    "MEDIUM": "security/medium",
    "LOW": "security/low",
}

# Slack webhook URL (from env, set via GitHub Actions secret)
SLACK_WEBHOOK_URL = os.environ.get("SLACK_WEBHOOK_URL", "")


def notify_slack(vulns):
    """Send a Slack notification for new CRITICAL CVEs."""
    if not SLACK_WEBHOOK_URL or not vulns:
        return

    count = len(vulns)
    cve_lines = []
    for v in vulns[:10]:
        cve_id = v["cve_id"]
        pkg = v.get("package", "unknown")
        fixed = v.get("fixed_version", "")
        fix_text = f" → fix in `{fixed}`" if fixed else " — no fix yet"
        cve_lines.append(f"• <https://nvd.nist.gov/vuln/detail/{cve_id}|{cve_id}> in `{pkg}`{fix_text}")

    if count > 10:
        cve_lines.append(f"_...and {count - 10} more_")

    text = "\n".join(cve_lines)
    payload = {
        "blocks": [
            {
                "type": "header",
                "text": {"type": "plain_text", "text": f"🚨 {count} new CRITICAL CVE{'s' if count != 1 else ''} detected"}
            },
            {
                "type": "section",
                "text": {"type": "mrkdwn", "text": text}
            },
            {
                "type": "context",
                "elements": [
                    {"type": "mrkdwn", "text": f"<https://github.com/{ISSUES_REPO}/issues?q=is%3Aissue+label%3Asecurity%2Fcritical+is%3Aopen|View open CRITICAL issues>"}
                ]
            }
        ]
    }

    try:
        import urllib.request
        req = urllib.request.Request(
            SLACK_WEBHOOK_URL,
            data=json.dumps(payload).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        resp = urllib.request.urlopen(req, timeout=10)
        print(f"  Slack notification sent ({resp.status})")
    except Exception as e:
        print(f"  WARN: Slack notification failed: {e}")


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


def cve_age_days(cve_id):
    """Estimate CVE age from its ID (CVE-YYYY-NNNNN)."""
    match = re.match(r"CVE-(\d{4})-", cve_id)
    if not match:
        return 0
    cve_year = int(match.group(1))
    now = datetime.now(timezone.utc)
    # Approximate: assume CVE was published Jan 1 of its year
    age = (now - datetime(cve_year, 1, 1, tzinfo=timezone.utc)).days
    return max(age, 0)


def is_dev_only(vuln):
    """Check if a CVE only affects dev/build images or test components."""
    # Fast path: scan.sh already tagged this
    if vuln.get("dev_only", False):
        return True

    repos = vuln.get("affected_repos", [])
    if not repos:
        return False

    for repo in repos:
        # Check component paths (e.g. "cozystack/cozystack:core/testing")
        if ":" in repo:
            component = repo.split(":", 1)[1]
            if any(pat in component for pat in DEV_COMPONENT_PATTERNS):
                continue  # this one is dev, check the rest
            return False  # at least one non-dev component
        else:
            return False  # whole-repo reference, not dev-only

    # All affected repos/components are dev-only
    return True


def classify_filter(vuln, triage_overrides):
    """Classify a CVE for filtering. Returns (skip, reason) or (False, None).

    skip=True means the CVE should not appear in critical/weekly reports.
    reason is a human-readable explanation for logging.
    """
    cve_id = vuln["cve_id"]
    severity = vuln.get("severity", "")

    # UNKNOWN severity — not in any known vulnerability DB
    if severity == "UNKNOWN":
        return True, "unknown severity"

    # Already triaged as resolved
    if cve_id in triage_overrides:
        status = triage_overrides[cve_id].get("status", "")
        if status in RESOLVED_STATUSES:
            return True, f"triaged as {status}"

    # Dev/build-only image or test component
    if is_dev_only(vuln):
        return True, "dev/build dependency only"

    # Unfixed upstream older than threshold
    if not vuln.get("fixed_version"):
        age = cve_age_days(cve_id)
        if age > UNFIXED_AGE_THRESHOLD_DAYS:
            return True, f"unfixed upstream ({age} days old)"

    return False, None


def save_report(subdir, filename, content):
    """Write a markdown report to reports/<subdir>/<filename>."""
    dirpath = os.path.join(REPORTS_DIR, subdir)
    os.makedirs(dirpath, exist_ok=True)
    filepath = os.path.join(dirpath, filename)
    with open(filepath, "w") as f:
        f.write(content)
    print(f"  Saved: reports/{subdir}/{filename}")
    return filepath


def create_ghsa_draft(vuln):
    """Create a draft GitHub Security Advisory for a CRITICAL CVE."""
    cve_id = vuln["cve_id"]
    package = vuln.get("package", "unknown")
    severity = vuln.get("severity", "CRITICAL").lower()
    description = vuln.get("description", "No description available.")[:1000]
    fixed = vuln.get("fixed_version", "")

    # Map severity to GHSA format
    ghsa_severity = severity if severity in ("critical", "high", "medium", "low") else "high"

    summary = f"{cve_id}: {vuln.get('title', f'Vulnerability in {package}')}"[:240]

    body = {
        "summary": summary,
        "description": f"""## Description

{description}

## Affected Component

Package: `{package}`
Installed version: `{vuln.get('installed_version', 'N/A')}`
{"Fixed in: `" + fixed + "`" if fixed else "No fix available yet."}

## References

- https://nvd.nist.gov/vuln/detail/{cve_id}

---
*Draft created automatically by the Cozystack security pipeline.*""",
        "severity": ghsa_severity,
        "cve_id": cve_id if cve_id.startswith("CVE-") else None,
    }

    # Remove None values
    body = {k: v for k, v in body.items() if v is not None}

    # Determine target repo — use the first affected non-component repo, or main
    affected = vuln.get("affected_repos", [])
    target_repos = sorted(set(r.split(":")[0] for r in affected))
    target_repo = target_repos[0] if target_repos else "cozystack/cozystack"

    cmd = ["gh", "api", f"/repos/{target_repo}/security-advisories",
           "--method", "POST",
           "-f", f"summary={body['summary']}",
           "-f", f"description={body['description']}",
           "-f", f"severity={body['severity']}"]

    if body.get("cve_id"):
        cmd.extend(["-f", f"cve_id={body['cve_id']}"])

    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        if result.returncode == 0:
            data = json.loads(result.stdout)
            url = data.get("html_url", "")
            print(f"  GHSA draft created: {url}")
            return url
        else:
            # 422 often means CVE ID already has an advisory, or permissions issue
            print(f"  WARN: GHSA draft failed for {cve_id}: {result.stderr[:200]}")
            return None
    except Exception as e:
        print(f"  WARN: GHSA error for {cve_id}: {e}")
        return None


def create_issue(vuln, report_path=None):
    """Create a GitHub Issue for a CVE in the security-scanner repo."""
    cve_id = vuln["cve_id"]
    severity = vuln.get("severity", "UNKNOWN")
    package = vuln.get("package", "unknown")
    fixed = vuln.get("fixed_version", "")
    title = vuln.get("title", "")

    issue_title = f"security_{severity.lower()}: {cve_id} — {title[:80]}" if title else f"security_{severity.lower()}: {cve_id} in {package}"

    refs = "\n".join(f"- {r}" for r in vuln.get("references", [])[:5])
    repos = "\n".join(f"- `{r}`" for r in vuln.get("affected_repos", []))
    fix_line = f"`{fixed}`" if fixed else "No fix available yet"
    report_link = f"\n**Report:** [{report_path}]({report_path})" if report_path else ""

    issue_body = f"""## {cve_id}

| Field | Value |
|-------|-------|
| **CVE** | [{cve_id}](https://nvd.nist.gov/vuln/detail/{cve_id}) |
| **Severity** | {severity} (CVSS {vuln.get('cvss_score', 'N/A')}) |
| **Package** | `{package}` |
| **Installed** | `{vuln.get('installed_version', 'N/A')}` |
| **Fixed in** | {fix_line} |
{report_link}

### Description

{vuln.get('description', 'No description available.')[:500]}

### Affected Components

{repos if repos else '- Unknown'}

### References

{refs if refs else '- None'}

### Triage Checklist

- [ ] Is this in a shipped artifact (not dev/test only)?
- [ ] Is the vulnerable code path reachable in our usage?
- [ ] Has the distro already backported the fix?
- [ ] Is a fix available upstream?

### Decision

<!-- Set a label and close when done:
  security/confirmed — real issue, needs fix
  security/false-positive — not applicable
  security/accepted-risk — accepted with justification
  security/in-progress — fix being worked on
  security/fixed — done
-->
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
           "--title", issue_title,
           "--body", issue_body] + label_args

    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        if result.returncode == 0:
            url = result.stdout.strip()
            print(f"  Issue created: {url}")
            return url
        else:
            print(f"  WARN: failed to create issue for {cve_id}: {result.stderr[:200]}")
            return None
    except Exception as e:
        print(f"  WARN: issue creation error for {cve_id}: {e}")
        return None


def build_critical_report(vuln, today):
    """Build markdown for a single critical CVE report."""
    refs = "\n".join(f"- {r}" for r in vuln.get("references", []))
    repos = "\n".join(f"- `{r}`" for r in vuln.get("affected_repos", []))
    fixed = vuln.get("fixed_version", "")
    fix_line = f"`{fixed}`" if fixed else "No fix available yet"
    cve_id = vuln["cve_id"]
    title = vuln.get("title", "No title")

    return f"""# security_critical: {cve_id}

**Date:** {today}
**Severity:** {vuln['severity']} (CVSS {vuln.get('cvss_score', 'N/A')})

## Vulnerability Details

| Field | Value |
|-------|-------|
| **CVE** | [{cve_id}](https://nvd.nist.gov/vuln/detail/{cve_id}) |
| **Severity** | {vuln['severity']} (CVSS {vuln.get('cvss_score', 'N/A')}) |
| **Package** | `{vuln.get('package', 'N/A')}` |
| **Installed version** | `{vuln.get('installed_version', 'N/A')}` |
| **Fixed in** | {fix_line} |

## Description

{vuln.get('description', 'No description available.')}

## Affected Components

{repos if repos else '- Unknown'}

## References

{refs if refs else '- None'}

## Recommended Action

{"Update `" + vuln.get('package', '') + "` to version `" + fixed + "` in the affected components listed above." if fixed else "Monitor for an upstream fix and update as soon as a patched version is available."}
"""


def build_weekly_report(vulns, today):
    """Build markdown for the weekly summary report."""
    by_severity = {}
    for v in vulns:
        sev = v["severity"]
        by_severity.setdefault(sev, []).append(v)

    summary_parts = []
    for sev in ["HIGH", "MEDIUM", "LOW"]:
        count = len(by_severity.get(sev, []))
        if count:
            summary_parts.append(f"{count} {sev}")
    summary = ", ".join(summary_parts) if summary_parts else "No new vulnerabilities"

    table_rows = []
    for v in sorted(vulns, key=lambda x: {"HIGH": 0, "MEDIUM": 1, "LOW": 2}.get(x["severity"], 9)):
        fixed = f"`{v['fixed_version']}`" if v.get("fixed_version") else "N/A"
        repos = ", ".join(f"`{r}`" for r in v.get("affected_repos", [])[:3])
        if len(v.get("affected_repos", [])) > 3:
            repos += f" +{len(v['affected_repos']) - 3} more"
        table_rows.append(
            f"| [{v['cve_id']}](https://nvd.nist.gov/vuln/detail/{v['cve_id']}) "
            f"| {v['severity']} "
            f"| `{v.get('package', '')}` "
            f"| {repos} "
            f"| {fixed} |"
        )

    table = "\n".join(table_rows) if table_rows else "| — | — | — | — | — |"

    return f"""# security_report: Weekly vulnerability summary ({today})

**Date:** {today}
**Total new vulnerabilities:** {len(vulns)}
**Breakdown:** {summary}

## New Vulnerabilities

| CVE | Severity | Package | Affected Repos | Fixed Version |
|-----|----------|---------|----------------|---------------|
{table}

## What to do

- Review each CVE and assess impact on your deployment
- Prioritize HIGH severity items and packages where a fixed version is available
- Monitor upstream for fixes where none are available yet
"""


def process_critical(scan_results, reported, triage_overrides):
    """Process CRITICAL CVEs — save individual reports immediately."""
    today = datetime.now(timezone.utc).strftime("%Y-%m-%d")
    new_critical = []
    filtered_count = 0

    for vuln in scan_results.get("vulnerabilities", []):
        cve_id = vuln["cve_id"]
        if vuln["severity"] != "CRITICAL":
            continue
        if cve_id in reported:
            continue
        skip, reason = classify_filter(vuln, triage_overrides)
        if skip:
            filtered_count += 1
            continue
        new_critical.append(vuln)

    if filtered_count:
        print(f"Filtered out {filtered_count} CRITICAL CVEs (triage/dev/unfixed)")

    if not new_critical:
        print("No new CRITICAL CVEs found.")
        return reported

    print(f"Found {len(new_critical)} new CRITICAL CVEs")

    for vuln in new_critical:
        cve_id = vuln["cve_id"]
        component = vuln.get("package", "unknown")
        content = build_critical_report(vuln, today)
        filename = f"{today}-{cve_id}.md"
        report_path = f"reports/critical/{filename}"

        print(f"\n  CRITICAL: {cve_id} ({component})")
        save_report("critical", filename, content)

        issue_url = create_issue(vuln, report_path)
        ghsa_url = create_ghsa_draft(vuln)

        affected = vuln.get("affected_repos", [])
        target_repos = sorted(set(r.split(":")[0] for r in affected))

        reported[cve_id] = {
            "severity": vuln["severity"],
            "reported_at": datetime.now(timezone.utc).isoformat(),
            "report_file": report_path,
            "issue_url": issue_url or "",
            "ghsa_url": ghsa_url or "",
            "repos": target_repos,
            "package": vuln.get("package", ""),
            "fixed_version": vuln.get("fixed_version", ""),
        }

    # Send Slack alert for all new CRITICAL CVEs in one message
    notify_slack(new_critical)

    return reported


def process_weekly(scan_results, reported, pending, triage_overrides):
    """Accumulate HIGH/MEDIUM/LOW CVEs into pending list."""
    new_count = 0
    filtered_count = 0

    for vuln in scan_results.get("vulnerabilities", []):
        cve_id = vuln["cve_id"]
        severity = vuln["severity"]

        if severity == "CRITICAL":
            continue
        if cve_id in reported:
            continue
        skip, reason = classify_filter(vuln, triage_overrides)
        if skip:
            filtered_count += 1
            continue
        if any(p["cve_id"] == cve_id for p in pending):
            continue

        pending.append(vuln)
        new_count += 1

    if filtered_count:
        print(f"Filtered out {filtered_count} HIGH/MEDIUM/LOW CVEs (triage/dev/unfixed)")
    print(f"Added {new_count} new HIGH/MEDIUM/LOW CVEs to pending weekly report (total pending: {len(pending)})")
    return pending


def send_weekly_report(reported, pending):
    """Save weekly summary report and create issues for each CVE."""
    if not pending:
        print("No pending CVEs for weekly report.")
        return reported, []

    today = datetime.now(timezone.utc).strftime("%Y-%m-%d")
    content = build_weekly_report(pending, today)
    filename = f"{today}-security-report.md"
    report_path = f"reports/weekly/{filename}"

    print(f"\nCreating weekly report with {len(pending)} CVEs...")
    save_report("weekly", filename, content)

    # Create individual Issues only for HIGH CVEs (CRITICAL already handled separately)
    high_vulns = [v for v in pending if v["severity"] == "HIGH"]
    other_vulns = [v for v in pending if v["severity"] != "HIGH"]

    if high_vulns:
        print(f"Creating issues for {len(high_vulns)} HIGH CVEs...")
    for vuln in high_vulns:
        issue_url = create_issue(vuln, report_path)
        reported[vuln["cve_id"]] = {
            "severity": vuln["severity"],
            "reported_at": datetime.now(timezone.utc).isoformat(),
            "report_file": report_path,
            "issue_url": issue_url or "",
            "repos": vuln.get("affected_repos", []),
            "package": vuln.get("package", ""),
            "fixed_version": vuln.get("fixed_version", ""),
        }

    # MEDIUM/LOW — report only, no individual Issues
    for vuln in other_vulns:
        reported[vuln["cve_id"]] = {
            "severity": vuln["severity"],
            "reported_at": datetime.now(timezone.utc).isoformat(),
            "report_file": report_path,
            "repos": vuln.get("affected_repos", []),
            "package": vuln.get("package", ""),
            "fixed_version": vuln.get("fixed_version", ""),
        }

    return reported, []


def main():
    parser = argparse.ArgumentParser(description="CVE report generator")
    parser.add_argument("scan_results", help="Path to scan-results.json")
    parser.add_argument("--weekly", action="store_true", help="Generate weekly report from pending CVEs")
    args = parser.parse_args()

    reported = load_json(REPORTED_FILE, {})
    pending = load_json(PENDING_FILE, [])
    triage_overrides = load_json(TRIAGE_FILE, {})

    with open(args.scan_results) as f:
        scan_results = json.load(f)

    triaged_count = len([v for v in triage_overrides.values() if v.get("status") in RESOLVED_STATUSES])
    print(f"Scan date: {scan_results.get('scan_date', 'unknown')}")
    print(f"Total CVEs in scan: {scan_results.get('total_vulnerabilities', 0)}")
    print(f"Already reported: {len(reported)}")
    print(f"Triage overrides: {len(triage_overrides)} ({triaged_count} resolved)")
    print()

    reported = process_critical(scan_results, reported, triage_overrides)
    pending = process_weekly(scan_results, reported, pending, triage_overrides)

    if args.weekly:
        print()
        reported, pending = send_weekly_report(reported, pending)

    save_json(REPORTED_FILE, reported)
    save_json(PENDING_FILE, pending)
    print(f"\nState saved. Reported: {len(reported)}, Pending: {len(pending)}")


if __name__ == "__main__":
    main()
