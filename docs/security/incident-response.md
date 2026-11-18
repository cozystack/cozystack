# Incident Response

This document is the public summary of how the Cozystack maintainers respond to
a confirmed security vulnerability or incident. Detailed operational procedures
(contact trees, credential inventories, and step-by-step checklists) are
maintained privately by the maintainers; this page describes the process and
the mechanisms it relies on so that reporters and users know what to expect.

It complements, and does not replace:

- [`SECURITY.md`](../../SECURITY.md) — how to report a vulnerability, response
  commitments, and the disclosure timeline.
- [`CONTRIBUTOR_LADDER.md`](../../CONTRIBUTOR_LADDER.md) — the offboarding
  procedure, including access revocation and credential rotation.

## Roles

For each incident the maintainers designate:

- **Incident lead** — a maintainer (typically a core maintainer) who coordinates
  triage, the fix, the release, and disclosure, and keeps the reporter informed.
- **Comms owner** — responsible for the public advisory and release-notes
  wording, and for any coordination with upstream projects and CNCF.
- **Fix owner(s)** — the maintainer(s) developing and reviewing the fix.

One person may hold more than one role on a small incident.

## Lifecycle

1. **Intake.** A report arrives through GitHub Private Vulnerability Reporting or
   a private maintainer contact (see `SECURITY.md`). Receipt is acknowledged
   within **3 business days**.
2. **Triage and severity.** The maintainers reproduce the issue and assign a
   CVSS v3.1 severity within **7 business days**. This drives the remediation
   target and disclosure window (see `SECURITY.md` → *Disclosure timeline*).
3. **Containment.** Where an immediate mitigation or workaround exists (a
   configuration change, a policy, or disabling an affected feature), it is
   documented and shared with the reporter and, if warranted, with affected
   adopters under embargo.
4. **Private fix development.** The fix is developed under a **GitHub Security Advisory** (private fork) so that code and discussion stay confidential until release. CI does not run there, so the fix is verified in the embargo mirror described under [Verifying a private fix](#verifying-a-private-fix) before it is merged from the advisory.
5. **Coordinated release.** The fix is shipped through the normal patch-release and backport process: the backport batch carries it onto the supported lines, the two latest minor releases, newest first, by the rules in [`docs/release/backports.md`](../release/backports.md), which a maintainer can override for a single fix. See the *Supported Versions* table in `SECURITY.md`.
6. **Disclosure.** A GHSA — and, when applicable, a CVE — is published. Public
   disclosure follows the outbound channels: GitHub Releases and release notes,
   the changelog, and the GHSA itself. The reporter is credited unless anonymity
   is requested. Disclosure happens no later than the coordinated-disclosure
   window committed in `SECURITY.md`.
7. **Access and credential hygiene.** If the incident involved a compromised or
   departing maintainer or leaked credential, the access-revocation and
   credential-rotation steps in `CONTRIBUTOR_LADDER.md` are executed (GitHub team
   access, org roles, Actions secrets, registry tokens, and App keys), completed
   within five business days.
8. **Retrospective.** After resolution the maintainers record what happened, the
   root cause, and any follow-up hardening, and open tracking issues for
   preventative work.

## Verifying a private fix

GitHub runs no CI in an advisory's temporary private fork: status checks do not run on its pull requests and no integration can reach it. Merging from the advisory also skips branch protection, so the required checks on `main` are not asked for either. The fix is therefore verified in a separate private repository, the embargo mirror, and the run there stands in for the checks the merge skips.

1. **Sync the mirror** right before use, not continuously: push the current `main` and every affected `release-X.Y` branch from `cozystack/cozystack` to the mirror, so the fix is tested on the same base it will be merged into.
2. **Open the pull request inside the mirror** from the same fix branch as the advisory's pull request, and wait for its CI.
3. **Record the run** in the advisory: a link to the green run and the commit it tested, which must be the head commit of the advisory's pull request. The merge itself creates a new commit on `main`, so compare against the pull request head, not against `main`.
4. **Merge from the advisory.** If `main` moved substantially while the run was in progress, sync and run again first.

Every job of a workflow that a push or a schedule starts is guarded to run only in `cozystack/cozystack`, so syncing the mirror does not build, publish, prune or release from it; `hack/workflow-repo-guard.bats` holds every such workflow to that guard. Pull request workflows still run, which is what the mirror is for. CodeQL is the exception to read with care: code scanning on a private repository needs a GitHub Code Security license, so without one it fails in the mirror for a reason unrelated to the fix. It is not a required check, and it runs on `main` after the merge.

The mirror must not be given the credentials of the public image registry. Pull request CI pushes every image it builds and the packages artifact, which carries the full chart tree, to that registry, and the registry is anonymously readable, tag listing included, so a run there would publish the fix before the release. Until pull request CI can push to a private registry instead, the mirror runs the jobs that need no registry credentials (planning, pre-commit and the unit tests), and the E2E suite runs after the merge.

## Severity and timelines

Severity classification (CVSS v3.1) and the target remediation and disclosure
timelines are defined once, authoritatively, in
[`SECURITY.md`](../../SECURITY.md) → *Disclosure timeline*. They are not
duplicated here to avoid drift.
