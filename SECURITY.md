# Security Policy

## Scope

This policy applies to the [`cozystack/cozystack`](https://github.com/cozystack/cozystack) repository and to release artifacts produced from it, including Cozystack core components, operators, packaged manifests, container images, and installation assets published by the project.

Cozystack integrates and ships many upstream cloud native components. If you believe a vulnerability originates in an upstream project rather than in Cozystack-specific code, packaging, defaults, or integration logic, please report it to the upstream project as well. If you are unsure, report it to Cozystack first and we will help route or coordinate the issue.

## Supported Versions

As of March 17, 2026, the Cozystack project maintains multiple release lines. Security fixes are prioritized for the latest stable release line and, when needed, backported to other supported lines.

| Version line | Status | Notes |
| --- | --- | --- |
| `v1.1.x` | Supported | Current stable release line. |
| `v1.0.x` | Supported | Previous stable release line; receives security and important maintenance fixes. |
| `v0.41.x` | Limited support | Legacy pre-v1 line during the v0 to v1 transition; critical security and upgrade-blocking fixes may be backported at maintainer discretion. |
| `< v0.41` | Not supported | Please upgrade to a supported release line before requesting a security fix. |
| `alpha`, `beta`, `rc` releases | Not supported | Pre-release builds are for testing and evaluation only. |

Supported versions may change over time as new release lines are cut. The authoritative source for current releases is the GitHub Releases page:

<https://github.com/cozystack/cozystack/releases>

## Reporting a Vulnerability

Please do **not** report security vulnerabilities through public GitHub issues, discussions, pull requests, Telegram, Slack, or other public community channels.

Please report vulnerabilities privately through one of the following channels, in order of preference:

1. **GitHub Private Vulnerability Reporting** — the preferred channel. Open the repository's **Security** tab, then **Advisories** → **Report a vulnerability** (<https://github.com/cozystack/cozystack/security/advisories/new>). This creates a confidential advisory visible only to you and the maintainers, and is the CNCF-recommended path for coordinated disclosure.
2. **Contact a maintainer** listed in [`MAINTAINERS.md`](MAINTAINERS.md) through an existing private channel you already have.
3. If you have neither, use a public community channel only to request a private contact path, without disclosing any vulnerability details.

Please do not include exploit details, credentials, tokens, private keys, customer data, or other sensitive material in any public message.

When reporting a vulnerability, please include as much of the following as possible:

- affected Cozystack version, tag, or commit
- affected component or package, for example operator, API server, dashboard, installer, or a packaged system component
- deployment environment and provider, for example bare metal, Hetzner, Oracle Cloud, or other infrastructure
- prerequisites and exact reproduction steps
- impact, attack scenario, and expected blast radius
- whether authentication, tenant access, cluster-admin access, or network adjacency is required
- known mitigations or workarounds
- whether you believe the issue also affects an upstream dependency

## What to Expect

The maintainers will aim to:

- acknowledge receipt within 3 business days
- perform an initial triage and severity assessment within 7 business days
- keep the reporter informed as the fix and disclosure plan are developed

Resolution timelines depend on severity, complexity, release branch applicability, and whether coordination with upstream projects is required.

### Disclosure timeline

The project follows a coordinated-disclosure window of **up to 90 days** from acknowledgement. If a fix or mitigation is not available within that window, the maintainers may publish the advisory (via GitHub Security Advisories) with the available details and any known workarounds, so that users are not left uninformed indefinitely. The window may be extended only by mutual agreement with the reporter, typically for issues that require coordination with upstream projects.

Target remediation timelines are guided by CVSS v3.1 severity:

| Severity (CVSS v3.1) | Target time to fix or mitigation |
| --- | --- |
| Critical (9.0–10.0) | ~14 days |
| High (7.0–8.9) | ~30 days |
| Medium (4.0–6.9) | ~90 days |
| Low (0.1–3.9) | next scheduled release |

These are targets, not guarantees; complex or upstream-coordinated issues may take longer.

## Disclosure Process

The Cozystack project follows a coordinated disclosure model.

- We ask reporters to keep details private until a fix or mitigation is available and users have had a reasonable opportunity to upgrade.
- When appropriate, maintainers may use GitHub Security Advisories or equivalent coordinated disclosure tooling to manage remediation and public disclosure.
- If appropriate, the project may request or publish a GHSA and/or CVE as part of the disclosure process.
- Fixes will normally be released in the supported version lines affected by the issue, subject to severity and feasibility.

Public disclosure will typically happen through one or more of the following:

- GitHub Releases and release notes
- project changelogs and documentation updates
- GitHub Security Advisories, when used for coordinated disclosure

## Project Security Practices

Security is part of the normal Cozystack development and release process. Current project practices include:

- maintainer-owned review through pull requests and `CODEOWNERS`
- automated pull request checks, including pre-commit validation, unit tests, builds, end-to-end testing, and static application security testing (CodeQL)
- release automation with patch releases, release branches, and backport workflows
- ongoing maintenance of packaged dependencies and platform integrations across supported release lines

Because Cozystack is an integration-heavy platform, some vulnerabilities may require coordination across multiple repositories or with upstream maintainers before a public fix can be released.

### Automated security analysis

Two scanners run continuously against this repository:

- **CodeQL** (static analysis). Runs on every pull request to `main`, on push to `main`, and on a weekly schedule. The Go database is built with CodeQL's `manual` build mode — each first-party module is compiled explicitly, so the analysis does not depend on the project `Makefile` (which fetches upstream tags) and stays reproducible. On a pull request CodeQL reports only alerts that are *new relative to `main`* and annotates them on the changed lines. New findings are expected to be resolved before merge — either by fixing the code, or, for a false positive or accepted risk, by dismissing the alert in the **Security → Code scanning** tab with a recorded reason (`False positive`, `Won't fix`, or `Used in tests`).
- **OpenSSF Scorecard** (supply-chain posture). Runs weekly and on branch-protection changes, and publishes results to the public Scorecard API at <https://scorecard.dev/viewer/?uri=github.com/cozystack/cozystack>. Scorecard results are intentionally **not** uploaded to GitHub code scanning: it posts one alert per check, which would bury CodeQL's first-party findings. The scorecard.dev badge is the canonical view.

CodeQL is intended to run as a required pull-request check, so that a newly introduced alert at error severity blocks merge until it is fixed or dismissed.

## Security Fixes and Announcements

Security fixes are published in normal release artifacts whenever possible. Users should monitor:

- GitHub Releases: <https://github.com/cozystack/cozystack/releases>
- project changelogs in this repository
- the Cozystack website and documentation: <https://cozystack.io>

## Out of Scope

The following are generally out of scope for private security reporting unless there is a clear Cozystack-specific impact:

- vulnerabilities in unsupported or end-of-life Cozystack versions
- issues that require access already equivalent to cluster-admin, node root, or direct infrastructure administrator privileges, unless they bypass an expected Cozystack security boundary
- vulnerabilities that exist only in an upstream dependency and are not introduced or materially worsened by Cozystack packaging, configuration, or defaults
- requests for security best-practice advice without a concrete vulnerability

## Credits

We appreciate responsible disclosure and will credit reporters in public advisories or release notes unless anonymous disclosure is requested.
