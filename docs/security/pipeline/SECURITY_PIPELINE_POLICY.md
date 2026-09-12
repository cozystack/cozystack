# Security Pipeline Policy

## Purpose

This document defines how the Cozystack project handles vulnerability findings from automated scanners, from initial detection through triage, remediation, and disclosure.

## Scope

This policy applies to:
- All non-fork repositories in the `cozystack` GitHub organization
- All container images shipped as part of Cozystack releases
- All third-party dependencies (Go modules, base images, Helm chart components)

## Ownership

The security pipeline is maintained by the Cozystack security team (subset of project maintainers with write access to this repository).

Security contact: **cncf-cozystack-security@lists.cncf.io**

## Pipeline Overview

```
Discovery → Scan → Normalize/Deduplicate → Triage → Route → Track → Fix → Disclose → Close
```

### 1. Discovery (automated, every 6 hours)

The pipeline automatically discovers components across all organization repositories:
- Go dependencies (`go.mod`)
- Dockerfile base images (`FROM` directives)
- Helm chart images (`values.yaml`, templates)
- Packages from `packages/apps/*`, `packages/system/*`, `packages/core/*`

### 2. Scan (automated)

Trivy scans all discovered components against known vulnerability databases (NVD, vendor advisories, GitHub Advisory Database).

### 3. Normalize / Deduplicate (automated)

- Same CVE across multiple images → single finding with all affected components listed
- Already-reported CVEs → skipped (tracked in `state/reported-cves.json`)
- Triage overrides → applied from `state/triage-overrides.json`

### 4. Triage (manual, by maintainers)

Every new finding must be triaged by a maintainer. See [SECURITY_FINDING_STATUS_MODEL.md](SECURITY_FINDING_STATUS_MODEL.md) for available statuses.

Triage questions:
1. Is this CVE in a **shipped artifact** or only in dev/build tooling?
2. Is the vulnerable **code path actually reachable** in our usage?
3. Has the OS distro already **backported** the fix?
4. Is a **fix available** upstream?
5. What is the **realistic impact** on a Cozystack deployment?

### 5. Routing (severity-based)

| Severity | Channel | Timing |
|----------|---------|--------|
| **Critical** | Individual report in `reports/critical/` + private notification to security team | Immediate (within 6 hours of detection) |
| **High** | Included in weekly maintainer digest | Weekly (Tuesday 06:00 CET) |
| **Medium** | Included in weekly maintainer digest | Weekly |
| **Low** | Included in weekly maintainer digest | Weekly |
| **Informational** | Monthly summary only | Monthly |

### 6. Fix Tracking

- CRITICAL: maintainer assigns owner within 1 business day
- Fixes are tracked in `state/triage-overrides.json` with status `in-progress`
- When fixed, status changes to `fixed` with the fix version noted

### 7. Disclosure

- **CRITICAL/HIGH (confirmed):** GitHub Security Advisory (draft, then published after fix)
- **MEDIUM/LOW:** Mentioned in monthly public security summary
- **Raw scanner output** is never published
- **False positives and accepted risks** are documented internally but not published

## What Is Private

- Raw scan results
- Unconfirmed CRITICAL/HIGH findings
- Exploit details before fix is available
- `state/` directory contents
- Triage discussions

## What Is Public (after triage and remediation)

- GitHub Security Advisories (post-fix)
- Monthly security summaries
- Release notes mentioning security fixes
- The existence of this security pipeline (not its internal reports)

## Maintainer Responsibilities

See [SECURITY_TRIAGE_MATRIX.md](SECURITY_TRIAGE_MATRIX.md) for detailed SLAs.

Maintainers must:
1. Review the weekly digest within 3 business days
2. Triage new CRITICAL findings within 1 business day
3. Assign an owner for confirmed CRITICAL/HIGH findings
4. Document triage decisions in `state/triage-overrides.json`
5. Not disclose CRITICAL/HIGH details publicly before a fix is available
