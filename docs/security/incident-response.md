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
4. **Private fix development.** The fix is developed under a **GitHub Security
   Advisory** (private fork) so that code and discussion stay confidential until
   release.
5. **Coordinated release.** The fix is shipped through the normal patch-release
   and backport process across all affected supported release lines
   (see the *Supported Versions* table in `SECURITY.md`).
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

## Severity and timelines

Severity classification (CVSS v3.1) and the target remediation and disclosure
timelines are defined once, authoritatively, in
[`SECURITY.md`](../../SECURITY.md) → *Disclosure timeline*. They are not
duplicated here to avoid drift.
