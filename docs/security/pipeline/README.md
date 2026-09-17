# Cozystack security-scanning pipeline — published policy and scripts

These are published copies of the Cozystack CVE-scanning pipeline's policy documents and
scripts, so that its method is auditable independently of the finding data it produces.
The **source of truth is the private `cozystack/security-scanner` repository**, which also
holds the per-finding reports and triage state that are **not** published here.

These copies correspond to `cozystack/security-scanner` PR #1004 (commit `b40462c1`), the
revision that carries the state-integrity, HIGH-path and scan-failure hardening described
below. They are kept byte-for-byte in step with it; once #1004 merges they match
`security-scanner` `main`, the revision that runs on schedule.

## Contents

- `SECURITY_PIPELINE_POLICY.md` — what the pipeline scans, on what schedule, and what is
  public vs private.
- `SECURITY_TRIAGE_MATRIX.md` — severity levels, triage clocks and fix targets.
- `SECURITY_FINDING_STATUS_MODEL.md` — the finding lifecycle and status model. The example
  identifiers in it are illustrative placeholders; real triage decisions live in the
  private state.
- `scripts/` — the discovery, scanning and reporting scripts (`discover.sh`, `scan.sh`,
  `report.py`, `monthly.py`, `sync_issues.py`, `backfill_issues.py`, and `statelib.py`, the
  shared crash-safe state I/O the readers import), published so the method can be audited.
  These are reference copies; the runnable versions live in the private repository against
  organization-scoped credentials.

## Disclosure rule

Severity-level aggregate counts are published (see [`../reports/`](../reports/)); individual
not-yet-fixed findings are not named, because a CVE ID plus an affected package plus this
project's public, version-pinned component list is enough to locate unpatched exposure in a
running deployment. That is why the per-finding reports and triage state in the private
repository are not mirrored here.

One exception, consistent with [`../reports/README.md`](../reports/README.md): a finding
with **no upstream fix available** is named, with mitigation guidance. There is nothing to
wait for, and silence serves nobody — an adopter needs to know when a mitigation is the only
available control. Findings awaiting a released fix stay aggregate-only until it ships.
