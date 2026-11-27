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
    "decided_at": "YYYY-MM-DDTHH:MM:SS+00:00",
    "closed_at": "YYYY-MM-DDTHH:MM:SS+00:00",
    "review_after": null
  },
  "CVE-YYYY-NNNN2": {
    "status": "accepted-risk",
    "reason": "example: CVE in a component or code path not exercised by Cozystack's usage",
    "decided_by": "maintainer-handle",
    "decided_at": "YYYY-MM-DDTHH:MM:SS+00:00",
    "closed_at": "YYYY-MM-DDTHH:MM:SS+00:00",
    "review_after": "YYYY-MM-DD"
  },
  "CVE-YYYY-NNNN3": {
    "status": "in-progress",
    "reason": "example: vulnerability with an upstream fix available; component bump in progress",
    "decided_by": "maintainer-handle",
    "decided_at": "YYYY-MM-DDTHH:MM:SS+00:00",
    "closed_at": "YYYY-MM-DDTHH:MM:SS+00:00",
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
| `status` | Yes | One of: `new`, `confirmed`, `in-progress`, `fixed`, `false-positive`, `accepted-risk`. `new` is the lifecycle entry state — a freshly-surfaced or awaiting-triage finding, including one the pipeline re-surfaces when an `accepted-risk` review window elapses |
| `reason` | Yes | Human-readable explanation of the decision |
| `decided_by` | Yes | GitHub handle of the maintainer |
| `decided_at` | Yes | Record time — when the pipeline recorded the decision. Full ISO-8601 (`YYYY-MM-DDTHH:MM:SS+00:00`); a bare `YYYY-MM-DD` is still accepted for hand-written entries. This is what the monthly report buckets on |
| `closed_at` | Yes | Event time — when the decision was actually made (the issue's close time for a synced decision, else the same as `decided_at`). Full ISO-8601. `sync_issues.py` orders decisions by this to break ties and reject a stale issue that reverts a newer decision, so a hand-written entry must carry it |
| `issue_number` / `issue_url` | For a decision synced from an issue | The issue whose label the record reflects. A label change on this issue is applied by the next triage sync even though its close time is unchanged; a different issue applies only if it closed later. When a review window elapses the pipeline moves these to the new re-review issue (or drops them when no issue is filed), so the old issue's label cannot revert the re-review |
| `review_after` | For `accepted-risk` | Date to re-evaluate (quarterly recommended) |
| `fix_version` | For `fixed` | Version where the fix is included |
| `fix_pr` | For `in-progress` / `fixed` | Link to the fix PR |

## Periodic Review

- `accepted-risk` findings must be reviewed every 90 days
- If `review_after` date has passed, the finding re-surfaces: CRITICAL and HIGH get a new triage issue, MEDIUM and LOW appear in the weekly report. Its record returns to `new` only once that issue exists (a failed issue creation is retried on the next run), and from then on it is the new issue, not the original one, whose label decides it
- `false-positive` is terminal: it carries no `review_after` and suppresses the finding indefinitely. There is no automatic re-check on Trivy database updates; if a false-positive needs revisiting (e.g. the finding's context changed), a maintainer re-opens or re-labels its issue and the next triage sync picks up the new decision.
