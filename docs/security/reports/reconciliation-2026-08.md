# Vulnerability backlog reconciliation — as of 2026-08-11

This is a one-off audit of every finding the Cozystack CVE-scanning pipeline has tracked since it started, reconciled against what the project actually shipped. It exists because the pipeline's own state was misleading in both directions, and because a due-diligence reviewer is entitled to the correction rather than the raw counts.

It supersedes any remediation figure implied by the monthly reports in this directory. The monthly reports state what happened in a month; this document states where the backlog actually stands.

## Why a reconciliation was needed

The pipeline records triage decisions by reading its own closed issues. **Nothing writes back to it from the main repository**, so it has never marked a single finding as fixed — `state/triage-overrides.json` holds 477 decisions and not one carries a fix version or a fix pull request. A finding whose component was upgraded two releases ago still sits in the tracker as confirmed and outstanding.

The consequence is that the tracker's headline numbers were wrong in both directions at once. They **overstated** open exposure, because remediation had happened and was invisible. They also **understated** how much of the backlog was never applicable, because a large class of unactionable findings had never been triaged at all.

## What the tracker holds

827 issues, of which **825 are findings** — two (`#386`, `#388`) are quarterly-security-review tracking tasks that the filters should have excluded and did not.

| State | Count |
| --- | --- |
| Confirmed — triaged as real, needs a fix | 332 |
| Dismissed — not applicable | 101 |
| Accepted risk | 61 |
| Awaiting a triage decision | 333 |
| Marked fixed | **0** |

All 332 confirmed findings were decided in a single bulk triage pass on 2026-06-11. No triage decisions were recorded in any other month.

## Applicability: how much of this was ever about what Cozystack ships

Classified in priority order, no overlaps. The already-dismissed findings supplied the precedents: each of the 162 dismissals carries a written justification, and those justifications establish rules that were then applied to the untriaged backlog.

| Class | Findings | Share |
| --- | --- | --- |
| Vulnerable code not present in executable form | 291 | 35% |
| Not attributed to any image — reachability cannot be assessed | 131 | 16% |
| Only in components a default installation does not deploy | 104 | 13% |
| Role unreachable — client library against a server-side flaw, interactive tool, build-time packaging | 65 | 8% |
| Platform or version gated — Windows-only, 32-bit-only, installed version below the introduced range | 18 | 2% |
| Already stale against the current tree | 10 | 1% |
| **Patch lag on a component a default installation does run** | **206** | **25%** |

**466 findings (57%) were never applicable or cannot be assessed.** The largest single block is 291 findings against packages that contain no executable vulnerable code — kernel headers being the dominant case. Cozystack containers never provide a running kernel; Talos does. The project's own maintainers applied exactly this reasoning 64 times to `kernel-headers` and 16 times to `perf`, and dismissed every one — while 194 findings against `linux-libc-dev`, which is the same argument with a different package name, sat untriaged. That is one missing filter rule accounting for a quarter of the tracker.

A second structural block: **68 findings report operating-system packages inside `system/kamaji`, whose final image is `gcr.io/distroless/static:nonroot`** — an image with no OS package layer at all. Nineteen of those are in the confirmed state, which is a triage error rather than a scanner artifact.

A third: **51 findings report Node ecosystem packages against `system/dashboard`**, whose shipped image is `nginx-unprivileged` plus a static build directory, with no Node runtime. Thirty-four are confirmed.

The 206 remaining patch-lag findings are legitimate hygiene: the package is present in a shipped image and a patch exists. None of them asserts a reachable attack path, and they should not be read as one. **Genuine reachable exposure in a stock installation is estimated at 15–40 findings, 2–5% of the tracker.** That is an estimate, not a measurement, and it is sensitive to two questions the tracker cannot answer — whether any shipped artifact carries a `runc` binary, and whether untrusted input reaches specific XML, ASN.1 and archive parsers inside vendored operator images.

## Remediation that actually happened

Cross-referencing the tracked findings against merged pull requests, release tags and the pipeline's own 18 weekly scan reports:

| Disposition of the 332 confirmed findings | Count |
| --- | --- |
| Fixed upstream and already pulled into the tree | 165 |
| Fixed by a change Cozystack authored | 26 |
| Fixed upstream, not yet pulled in | 10 fully, 27 partially |
| No upstream fix exists | 26 |
| Not attributable to an image, so undeterminable | 78 |

Of the 165, **34 are verified by exact version comparison** against the tree; the remaining 131 rest on the carrier image having been rebuilt, which is strong evidence but not proof without a re-scan.

The 26 fixed by Cozystack's own work are a distinct and easily missed category: re-vendoring the dashboard console in-tree ([#2963](https://github.com/cozystack/cozystack/pull/2963)) did not upgrade the vulnerable npm dependencies, it **removed them from existence**. Separately, [#3033](https://github.com/cozystack/cozystack/pull/3033) forced module versions during the Kamaji build because no upstream Kamaji tag carried the fixes yet.

**Remediation was a single wave, not a steady stream.** Twenty-two pull requests between 2026-06-15 and 2026-06-30 account for essentially all of it: 162 confirmed findings and 40 awaiting-triage findings were carried away in that fortnight. Zero in April, seven in May, one in July, none in August.

The changes that did the most work were largely invisible to identifier-based searching, because they name no CVE at all:

| Pull request | What it changed | Confirmed findings carried |
| --- | --- | --- |
| [#2963](https://github.com/cozystack/cozystack/pull/2963) | dashboard console vendored in-tree; vulnerable npm dependencies cease to exist | 31 |
| [#2964](https://github.com/cozystack/cozystack/pull/2964) | Keycloak 26.5.2 → 26.6.3 | 25 |
| [#2992](https://github.com/cozystack/cozystack/pull/2992) | fdb-operator v2.30.0 | 17 |
| [#2966](https://github.com/cozystack/cozystack/pull/2966) | Harbor chart 1.19.1 / 2.15.1 | 14 |
| [#3009](https://github.com/cozystack/cozystack/pull/3009) | first-party Alpine bases → 3.24 across six images | 14 |
| [#2834](https://github.com/cozystack/cozystack/pull/2834), [#2916](https://github.com/cozystack/cozystack/pull/2916) | SeaweedFS 4.31 | 13 |
| [#3014](https://github.com/cozystack/cozystack/pull/3014) | managed Kubernetes patch versions | 11 |
| [#2494](https://github.com/cozystack/cozystack/pull/2494) | Go modules and `golang:1.26` in first-party Dockerfiles | 7 |
| [#2972](https://github.com/cozystack/cozystack/pull/2972) | Cilium 1.19.3 → 1.19.5 | 6 |
| [#3019](https://github.com/cozystack/cozystack/pull/3019) | `golang.org/x/crypto` → 0.52.0 | 4 |
| [#2974](https://github.com/cozystack/cozystack/pull/2974) | Velero chart 12.0.3 | 4 |
| [#2941](https://github.com/cozystack/cozystack/pull/2941) | `golang:1.26` across eight remaining builder images | 3 directly |

The Go toolchain thread deserves separate mention because the weekly reports evidence it independently: findings against ancient Go lines (1.15 through 1.20) appear only in the 2026-04-07 and 2026-05-05 scans and never again. After the toolchain was brought current, the only new `stdlib` findings require a version newer than the current pin. All 62 confirmed `stdlib` findings are formally satisfied by the toolchain now in the tree.

### Fixes available and not yet taken

Small, specific and actionable today:

| Component | In tree | Fix requires | Findings |
| --- | --- | --- | --- |
| `nats-server` | appVersion 2.11.10 | 2.11.14 / 2.11.15 | 6 |
| CoreDNS image | v1.12.4 | 1.14.2 | 1 |
| OpenBao | v2.5.1 | 2.5.4 | 1 |
| `fast-uri` | 3.1.0 | 3.1.1 / 3.1.2 | 2 |

## Fixes released with a named identifier

Both are already public in merged commit subjects, so naming them here adds no exposure.

| CVE | Component | Upstream fix | Shipped in Cozystack | Pull requests |
| --- | --- | --- | --- | --- |
| [CVE-2026-31431](https://nvd.nist.gov/vuln/detail/CVE-2026-31431) | Talos Linux | v1.12.7 | v1.2.4, v1.3.2, v1.4.0 | [#2545](https://github.com/cozystack/cozystack/pull/2545), [#2547](https://github.com/cozystack/cozystack/pull/2547), [#2548](https://github.com/cozystack/cozystack/pull/2548) |
| [CVE-2026-53359](https://nvd.nist.gov/vuln/detail/CVE-2026-53359) | Talos Linux | v1.13.6 | v1.6.0, v1.6.1 | [#3240](https://github.com/cozystack/cozystack/pull/3240), [#3269](https://github.com/cozystack/cozystack/pull/3269) |

## Defects found in the pipeline itself

The audit found more wrong with the tooling than with the fixing. All of these are the pipeline's, not the maintainers':

1. **No write-back.** Nothing observes the main repository, so nothing is ever closed as fixed and the confirmed state grows monotonically and permanently.
2. **Deduplication by CVE identifier alone.** `scan.sh` keys on the CVE, so `package`, `installed_version` and `fixed_version` are taken from whichever scan file sorted first and are never updated afterwards. This is why `stdlib` findings show a single installed version across sixty carriers, and why some show none. The 825 findings reduce to **247 distinct `(package, installed version)` roots**; the five largest roots are 41% of the whole backlog. One stale Go binary produced 62 issues; one `linux-libc-dev` produced 194.
3. **Broken image attribution in the first baseline.** Component matching normalises image references with a substitution that breaks on digest-pinned refs, which is the mechanical cause of the 95 findings recorded with `Unknown` components — all from the 2026-04-07 baseline. One hundred and nine unattributed findings were nonetheless closed as confirmed, permanently recording a problem nobody can locate.
4. **Missing kernel-package filter**, described above: 258 records, a quarter of the tracker.
5. **No distroless or final-layer awareness**, so OS packages are reported against images that have no OS layer and npm packages against images with no Node runtime.
6. **The documented "no severity assigned" filter does not work**, which is how two process-tracking issues entered the finding set.
7. **The documented "dev/test only" filter tests the wrong condition** — it excludes a finding when no component is dev/test, rather than when all components are.

## What is being done about it

In priority order, and the order is deliberate: the first item is a person, not a tool.

1. **Fill the rotating Security Champion role.** It is defined in the pipeline's governance document with a six-month term and a handover overlap, and it is currently unassigned. Nobody owns the triage clock, which is why 494 decisions landed in one day and none since. No amount of tooling substitutes for this.
2. **Triage the genuinely urgent findings**, which the noise has been hiding. The audit surfaced several open findings whose severity and reachability warrant immediate attention ahead of the backlog, including a privilege-escalation path relevant to multi-tenant deployments and an authentication fail-open in a shipped component. These are being handled through the private channel rather than named here, per the disclosure rule below.
3. **Close the write-back loop.** Findings should be keyed on `(vulnerability id, package, purl, target type, artifact reference by digest)` rather than on the CVE alone, and closed when the key is absent from two consecutive scans — distinguishing *no longer shipped* from *fixed*, so that deleting an image is never read as remediation. Fix attribution can then be derived deterministically by searching the main repository's history for the superseded digest, rather than by matching keywords in pull request titles.
4. **Add the input filters** for kernel packages, header and development packages, distroless targets, final-layer-only language ecosystems, and a version-arithmetic gate that rejects a finding whose installed version predates the range the vulnerability was introduced in.
5. **Deduplicate by root rather than by CVE**, which reduces the triage workload by roughly a factor of three without discarding information.
6. **Re-scan against current versions**, which settles the 131 findings whose remediation is evidenced only by a carrier rebuild, and retire the unattributable baseline findings.

## Disclosure rule

Aggregate counts by severity are published. An individual finding is named once its fix is merged and released.

Two revisions to that rule follow from this audit, and both make it more open rather than less. First, **findings with no upstream fix will be named, with mitigation guidance**, because there is nothing to wait for and withholding them serves nobody: adopters need to know that a mitigation is the only available control. Roughly a fifth of the backlog is in that state. Second, per-month attribution of remediation has been removed from the monthly reports, because a severity-banded count of still-open findings crossed with a single month's list of component bumps — which is publicly enumerable from the commit log — narrows the search for unpatched exposure more than a bare count does. That construct was a leak the original rule did not anticipate.

What stays embargoed is narrow: findings where Cozystack's own packaging, defaults or integration logic introduces or worsens the exposure. Those have no upstream advisory, the project is the only source, and they follow the coordinated-disclosure process in [`SECURITY.md`](../../../SECURITY.md).

## Provenance

Generated 2026-08-11 from the tracker state on that date, cross-referenced against 660 pull requests merged into `cozystack/cozystack` between 2026-04-01 and 2026-08-10, the release tags carrying them, and the pipeline's 18 weekly scan reports. The tracker itself is not public, so these counts are self-attested; publishing the pipeline's policy documents and scanning scripts, so that the method can be audited independently of the numbers, is outstanding work.
