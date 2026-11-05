# Security Triage Matrix

## Severity → Action → SLA

| | Critical | High | Medium | Low | Informational |
|---|---|---|---|---|---|
| **Acknowledgment** | 4 hours | 1 business day | 3 business days | Weekly digest | Monthly summary |
| **Triage** | 1 business day | 3 business days | 10 business days | 30 days | No action required |
| **Owner assigned** | 1 business day | 5 business days | Best effort | Best effort | N/A |
| **Remediation plan** | 2 business days | 10 business days | Next release | Best effort | N/A |
| **Fix target** | 7 days | 30 days | 90 days | No hard deadline | N/A |
| **Disclosure** | After fix + 7 days | After fix + 14 days | Monthly report | Monthly report | Monthly report |

## Routing

| Severity | Immediate action | Report location | Notification |
|---|---|---|---|
| **Critical** | Individual report saved to `reports/critical/` | `reports/critical/YYYY-MM-DD-CVE-XXXX.md` | Security team notified directly |
| **High** | Added to weekly digest | `reports/weekly/YYYY-MM-DD-security-report.md` | Weekly maintainer digest |
| **Medium** | Added to weekly digest | `reports/weekly/YYYY-MM-DD-security-report.md` | Weekly maintainer digest |
| **Low** | Added to weekly digest | `reports/weekly/YYYY-MM-DD-security-report.md` | Weekly maintainer digest |
| **Informational** | Logged only | Monthly summary | No direct notification |

## Triage Decision Tree

```
New CVE found by scanner
  │
  ├─ Is it in a shipped artifact (not dev/test only)?
  │   ├─ No → status: false-positive (reason: "dev dependency only")
  │   └─ Yes ↓
  │
  ├─ Is the vulnerable code path reachable in our usage?
  │   ├─ No → status: accepted-risk (reason: "code path not reachable")
  │   └─ Yes / Uncertain ↓
  │
  ├─ Has the distro already backported the fix?
  │   ├─ Yes → status: false-positive (reason: "distro backport applied")
  │   └─ No ↓
  │
  ├─ Is a fix available upstream?
  │   ├─ Yes → status: confirmed, assign owner, schedule update
  │   └─ No ↓
  │
  └─ Is the CVE older than 365 days with no fix?
      ├─ Yes → status: accepted-risk (reason: "unfixed upstream, monitoring")
      └─ No → status: confirmed, monitor upstream for fix
```

## Escalation

- If a CRITICAL finding is not triaged within 1 business day → escalate to all maintainers
- If a confirmed CRITICAL has no fix plan within 2 business days → discuss workaround/mitigation
- If a HIGH finding is not triaged within 5 business days → auto-escalate in next digest
