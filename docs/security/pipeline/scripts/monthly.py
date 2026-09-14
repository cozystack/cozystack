#!/usr/bin/env python3
"""
monthly.py — Generate a public monthly security summary.

This report contains only triaged, post-review findings — not raw scanner output.
It is suitable for publishing on the Cozystack website or in the main repository.

Reads state/reported-cves.json and state/triage-overrides.json to produce
a summary of what was fixed, what is confirmed/in-progress, and what was
accepted as risk during the past month.
"""

import json
import os
from collections import Counter
from datetime import datetime, timezone, timedelta

REPO_ROOT = os.path.join(os.path.dirname(__file__), "..")
STATE_DIR = os.path.join(REPO_ROOT, "state")
REPORTS_DIR = os.path.join(REPO_ROOT, "reports")
REPORTED_FILE = os.path.join(STATE_DIR, "reported-cves.json")
TRIAGE_FILE = os.path.join(STATE_DIR, "triage-overrides.json")


def load_json(path, default):
    try:
        with open(path) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return default


def save_report(content, filename):
    dirpath = os.path.join(REPORTS_DIR, "monthly")
    os.makedirs(dirpath, exist_ok=True)
    filepath = os.path.join(dirpath, filename)
    with open(filepath, "w") as f:
        f.write(content)
    print(f"Saved: reports/monthly/{filename}")
    return filepath


def main():
    now = datetime.now(timezone.utc)
    # Report covers the previous month
    first_of_this_month = now.replace(day=1, hour=0, minute=0, second=0, microsecond=0)
    last_month_end = first_of_this_month - timedelta(seconds=1)
    first_of_last_month = last_month_end.replace(day=1, hour=0, minute=0, second=0, microsecond=0)

    month_label = first_of_last_month.strftime("%B %Y")
    month_start = first_of_last_month.isoformat()
    month_end = first_of_this_month.isoformat()

    print(f"Generating monthly report for: {month_label}")
    print(f"  Period: {month_start} to {month_end}")

    reported = load_json(REPORTED_FILE, {})
    triage = load_json(TRIAGE_FILE, {})

    # Categorize all CVEs by their triage status
    fixed_this_month = []
    confirmed_open = []
    in_progress = []
    accepted_risk = []
    false_positives_count = 0
    new_this_month = []

    for cve_id, data in reported.items():
        severity = data.get("severity", "UNKNOWN")
        triage_entry = triage.get(cve_id, {})
        status = triage_entry.get("status", "new")

        # Check if reported this month
        reported_at = data.get("reported_at", "")
        is_this_month = month_start <= reported_at < month_end if reported_at else False

        if status == "fixed":
            decided_at = triage_entry.get("decided_at", "")
            # Check if fixed this month
            if decided_at and first_of_last_month.strftime("%Y-%m") <= decided_at[:7] <= last_month_end.strftime("%Y-%m"):
                fixed_this_month.append({
                    "cve_id": cve_id,
                    "severity": severity,
                    "package": data.get("package", ""),
                    "fixed_version": data.get("fixed_version", triage_entry.get("fix_version", "")),
                    "repos": data.get("repos", []),
                })
        elif status == "confirmed":
            confirmed_open.append({
                "cve_id": cve_id, "severity": severity,
                "package": data.get("package", ""),
            })
        elif status == "in-progress":
            in_progress.append({
                "cve_id": cve_id, "severity": severity,
                "package": data.get("package", ""),
                "fix_pr": triage_entry.get("fix_pr", ""),
            })
        elif status == "accepted-risk":
            accepted_risk.append({
                "cve_id": cve_id, "severity": severity,
                "package": data.get("package", ""),
                "reason": triage_entry.get("reason", ""),
            })
        elif status == "false-positive":
            false_positives_count += 1

        if is_this_month:
            new_this_month.append({
                "cve_id": cve_id, "severity": severity,
                "package": data.get("package", ""),
            })

    # Count new by severity
    new_by_sev = Counter(v["severity"] for v in new_this_month)
    total_tracked = len(reported)
    total_triaged = len(triage)

    # Build report
    new_summary_parts = []
    for sev in ["CRITICAL", "HIGH", "MEDIUM", "LOW"]:
        if new_by_sev.get(sev):
            new_summary_parts.append(f"{new_by_sev[sev]} {sev}")
    new_summary = ", ".join(new_summary_parts) if new_summary_parts else "none"

    # Fixed table
    if fixed_this_month:
        fixed_rows = []
        for v in sorted(fixed_this_month, key=lambda x: x["severity"]):
            repos = ", ".join(f"`{r}`" for r in v["repos"][:3]) if v["repos"] else "—"
            fixed_rows.append(
                f"| [{v['cve_id']}](https://nvd.nist.gov/vuln/detail/{v['cve_id']}) "
                f"| {v['severity']} | `{v['package']}` | `{v.get('fixed_version', 'N/A')}` | {repos} |"
            )
        fixed_table = "\n".join(fixed_rows)
    else:
        fixed_table = "| — | — | — | — | — |"

    # In-progress table
    if in_progress:
        ip_rows = []
        for v in sorted(in_progress, key=lambda x: {"CRITICAL": 0, "HIGH": 1}.get(x["severity"], 2)):
            pr_link = f"[PR]({v['fix_pr']})" if v.get("fix_pr") else "—"
            ip_rows.append(
                f"| [{v['cve_id']}](https://nvd.nist.gov/vuln/detail/{v['cve_id']}) "
                f"| {v['severity']} | `{v['package']}` | {pr_link} |"
            )
        ip_table = "\n".join(ip_rows)
    else:
        ip_table = "| — | — | — | — |"

    # Accepted risk table
    if accepted_risk:
        ar_rows = []
        for v in sorted(accepted_risk, key=lambda x: x["cve_id"]):
            reason = v.get("reason", "")[:80]
            ar_rows.append(
                f"| [{v['cve_id']}](https://nvd.nist.gov/vuln/detail/{v['cve_id']}) "
                f"| {v['severity']} | `{v['package']}` | {reason} |"
            )
        ar_table = "\n".join(ar_rows)
    else:
        ar_table = "| — | — | — | — |"

    content = f"""# Cozystack Security Summary — {month_label}

> This is a public monthly summary of security activities in the Cozystack project.
> It covers triaged findings only — not raw scanner output.

## Overview

| Metric | Count |
|--------|-------|
| New vulnerabilities detected | {len(new_this_month)} ({new_summary}) |
| Fixed this month | {len(fixed_this_month)} |
| In progress | {len(in_progress)} |
| Confirmed (awaiting fix) | {len(confirmed_open)} |
| Accepted risk | {len(accepted_risk)} |
| False positives dismissed | {false_positives_count} |
| Total tracked | {total_tracked} |
| Total triaged | {total_triaged} |

## Security Updates Released

| CVE | Severity | Package | Fixed Version | Components |
|-----|----------|---------|---------------|------------|
{fixed_table}

## In Progress

| CVE | Severity | Package | Fix PR |
|-----|----------|---------|--------|
{ip_table}

## Accepted Risks

| CVE | Severity | Package | Reason |
|-----|----------|---------|--------|
{ar_table}

## How to Report

If you discover a security vulnerability in Cozystack:

1. Use [GitHub Private Vulnerability Reporting](https://github.com/cozystack/cozystack/security/advisories/new)
2. Or email **cncf-cozystack-security@lists.cncf.io**

See [SECURITY.md](https://github.com/cozystack/cozystack/blob/main/SECURITY.md) for full details.

---
*Generated on {now.strftime("%Y-%m-%d")} by the Cozystack security pipeline.*
"""

    filename = f"{first_of_last_month.strftime('%Y-%m')}-security-summary.md"
    save_report(content, filename)

    # Also output JSON for the website
    website_json = {
        "month": month_label,
        "generated_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "new_count": len(new_this_month),
        "fixed": [{"cve_id": v["cve_id"], "severity": v["severity"], "package": v["package"], "fixed_version": v.get("fixed_version", "")} for v in fixed_this_month],
        "in_progress": [{"cve_id": v["cve_id"], "severity": v["severity"], "package": v["package"]} for v in in_progress],
        "accepted_risk": [{"cve_id": v["cve_id"], "severity": v["severity"], "package": v["package"], "reason": v.get("reason", "")[:100]} for v in accepted_risk],
        "stats": {
            "total_tracked": total_tracked,
            "total_triaged": total_triaged,
            "false_positives": false_positives_count,
        },
    }

    json_path = os.path.join(REPORTS_DIR, "monthly", "latest.json")
    with open(json_path, "w") as f:
        json.dump(website_json, f, indent=2)
    print(f"Saved: reports/monthly/latest.json")

    print(f"\nSummary for {month_label}:")
    print(f"  New: {len(new_this_month)}, Fixed: {len(fixed_this_month)}, In-progress: {len(in_progress)}")
    print(f"  Confirmed: {len(confirmed_open)}, Accepted risk: {len(accepted_risk)}, False positives: {false_positives_count}")


if __name__ == "__main__":
    main()
