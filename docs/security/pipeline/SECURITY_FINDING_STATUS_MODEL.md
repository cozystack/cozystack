# Security Finding Status Model

## Statuses

| Status | Meaning | Who sets it | Next action |
|--------|---------|-------------|-------------|
| `new` | Scanner found this CVE, no human has reviewed it yet | Automated (default) | Triage needed |
| `confirmed` | Maintainer verified this is a real, applicable vulnerability | Maintainer | Assign owner, plan fix |
| `in-progress` | Fix is being worked on | Maintainer / owner | Track to completion |
| `fixed` | Fix has been released | Maintainer | Close, update advisory if needed |
| `false-positive` | Scanner finding does not apply to our context | Maintainer | Document reason, no further action |
| `accepted-risk` | Real vulnerability, but we consciously accept the risk | Maintainer | Document reason, review periodically |

## Lifecycle

```
new → confirmed → in-progress → fixed
new → false-positive
new → accepted-risk
confirmed → accepted-risk  (if fix becomes impractical)
accepted-risk → confirmed  (if circumstances change)
```

## Recording Triage Decisions

Triage decisions are stored in `state/triage-overrides.json`:

```json
{
  "CVE-YYYY-NNNN1": {
    "status": "false-positive",
    "reason": "example: library CVE in a base image; the distribution has backported the fix",
    "decided_by": "maintainer-handle",
    "decided_at": "YYYY-MM-DD",
    "review_after": null
  },
  "CVE-YYYY-NNNN2": {
    "status": "accepted-risk",
    "reason": "example: CVE in a component or code path not exercised by Cozystack's usage",
    "decided_by": "maintainer-handle",
    "decided_at": "YYYY-MM-DD",
    "review_after": "YYYY-MM-DD"
  },
  "CVE-YYYY-NNNN3": {
    "status": "in-progress",
    "reason": "example: vulnerability with an upstream fix available; component bump in progress",
    "decided_by": "maintainer-handle",
    "decided_at": "YYYY-MM-DD",
    "fix_version": "x.y.z",
    "fix_pr": "https://github.com/cozystack/cozystack/pull/NNNN",
    "review_after": null
  }
}
```

> The identifiers above are illustrative placeholders showing the record shape only.
> Live triage decisions are kept in the security pipeline's private state, since a real
> CVE crossed with this project's public component list can locate unpatched exposure.

## Required Fields

| Field | Required | Description |
|-------|----------|-------------|
| `status` | Yes | One of: `confirmed`, `in-progress`, `fixed`, `false-positive`, `accepted-risk` |
| `reason` | Yes | Human-readable explanation of the decision |
| `decided_by` | Yes | GitHub handle of the maintainer |
| `decided_at` | Yes | Date of the decision (YYYY-MM-DD) |
| `review_after` | For `accepted-risk` | Date to re-evaluate (quarterly recommended) |
| `fix_version` | For `fixed` | Version where the fix is included |
| `fix_pr` | For `in-progress` / `fixed` | Link to the fix PR |

## Periodic Review

- `accepted-risk` findings must be reviewed every 90 days
- If `review_after` date has passed, the finding is flagged in the weekly digest
- `false-positive` findings are re-checked automatically when Trivy DB updates (if the CVE reappears with different metadata, it becomes `new` again)
