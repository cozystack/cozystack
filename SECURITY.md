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

The preferred way to report a vulnerability is through **GitHub Private Vulnerability Reporting**:

1. Go to the **Security** tab of the affected repository (or use [this link for the main repository](https://github.com/cozystack/cozystack/security/advisories/new)).
2. Click **"Report a vulnerability"** and fill in the details.
3. The report will be visible only to the repository maintainers.

Alternatively, you can email the security team directly at **cncf-cozystack-security@lists.cncf.io**. This is a private mailing list monitored by project maintainers.

If neither GitHub nor email works for you, you may contact a project maintainer listed in `CODEOWNERS` through an existing private channel. If you do not already have a private maintainer contact, use a public community channel only to request a private contact path, without disclosing any vulnerability details.

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
- automated pull request checks, including pre-commit validation, unit tests, builds, and end-to-end testing
- release automation with patch releases, release branches, and backport workflows
- ongoing maintenance of packaged dependencies and platform integrations across supported release lines
- automated vulnerability scanning of all organization repositories and container images using Trivy
- GitHub Private Vulnerability Reporting enabled on all public repositories
- GitHub Secret Scanning and Push Protection enabled on all repositories
- two-factor authentication required for all organization members
- monthly public security summaries covering triaged findings

Because Cozystack is an integration-heavy platform, some vulnerabilities may require coordination across multiple repositories or with upstream maintainers before a public fix can be released.

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
