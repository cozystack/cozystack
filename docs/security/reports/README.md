# Monthly security reports

Public reporting on vulnerability handling in the Cozystack project, generated from the tracked findings of the organization-wide CVE-scanning pipeline described in [`SECURITY.md`](../../../SECURITY.md).

**Start with [the backlog reconciliation](reconciliation-2026-08.md).** The monthly reports state what happened in a month, counted from the pipeline's own records. The pipeline has no write-back from the main repository, so it has never marked a finding as fixed and its outstanding counts overstate real exposure. The reconciliation establishes what was actually remediated, how much of the backlog was never applicable, and what is defective in the tooling. The monthly figures are not interpretable without it.

| Document | Covers |
| --- | --- |
| [Backlog reconciliation](reconciliation-2026-08.md) | Every finding since the pipeline started, audited against what shipped. As of 2026-08-11 |
| [August 2026](2026-08.md) | Incomplete; data cut off 2026-08-10 |
| [July 2026](2026-07.md) | |
| [June 2026](2026-06.md) | |
| [May 2026](2026-05.md) | |
| [April 2026](2026-04.md) | First month; an initial load rather than a discovery rate |

## What these reports contain

Findings are reported as **counts by severity**. An individual finding is named once its fix is merged and released — both such findings to date appear in the reconciliation and in the month they shipped.

Two deliberate exceptions to that reticence, both arrived at by reviewing the rule rather than applying it mechanically. **Findings with no upstream fix will be named, with mitigation guidance**, because there is nothing to wait for and silence serves nobody: an adopter needs to know when a mitigation is the only available control. And **remediation is not attributed to individual months**, because a count of still-open findings crossed with one month's list of component bumps — which anybody can enumerate from the commit log — narrows the search for unpatched exposure further than a bare count does.

What stays embargoed is narrow: findings where Cozystack's own packaging, defaults or integration logic introduces or worsens the exposure. There the project is the only source, and the process is the coordinated disclosure in [`SECURITY.md`](../../../SECURITY.md).

## Scope of the counts

The counts cover findings from the pipeline, which scans **every non-fork, non-archived repository in the `cozystack` organization** — not this repository alone. They are filtered to what the pipeline treats as actionable: development-and-build-only dependencies, findings whose upstream has had no fix for over a year, findings already carrying a triage decision, and findings with no severity assigned are excluded before a finding is tracked. Two of those filters do not work as documented, which the reconciliation records.

Vulnerabilities reported privately against Cozystack's own code, packaging, defaults or integration logic follow the separate process in [`SECURITY.md`](../../../SECURITY.md), with its own response targets, and are disclosed through GitHub Security Advisories rather than here.

## Provenance and its limits

These figures are self-attested. The tracker is private, because it holds per-finding detail for exposure that is not yet fixed, so a reader cannot currently reproduce the counts. Publishing the pipeline's policy documents and scanning scripts — which contain no finding data — so that the method can be audited independently of the numbers is outstanding work. Until that lands, treat the numbers as a project statement rather than as verified evidence.
