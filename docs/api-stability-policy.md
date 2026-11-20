# API Stability and Deprecation Policy

This document states what Cozystack promises to users about the interfaces they build on: which surfaces are covered, how stable each one is, how much notice a breaking change gets, and which upgrades are supported. It is the contract; the procedures that implement it live in [release.md](release.md) and [deprecating-applications.md](deprecating-applications.md).

## Covered surfaces

| Surface | What it includes |
| --- | --- |
| Kubernetes APIs | Every `*.cozystack.io` API group served by the platform, aggregated or CRD, except `internal.cozystack.io` |
| Application parameters | The parameters of each catalog application, which are the `spec` of its `apps.cozystack.io` objects |
| Platform configuration | Installer and `Platform` values, bundle and variant names, `PackageSource` names |
| Go API module | `github.com/cozystack/cozystack/api/apps/v1alpha1`, tagged with every stable release |
| Upgrade paths | Which version-to-version upgrades are supported |

Not covered: `internal.cozystack.io`, dashboard layout, metric and alert names, and anything documented as experimental. These may change in any minor release, and changes are still listed in release notes.

For Cozystack, application parameters matter as much as API versions: renaming a parameter breaks every manifest and GitOps repository that sets it, even when the API version string does not change. They are therefore a covered surface in their own right. There is no per-application maturity field in `ApplicationDefinition` yet, so all application parameters share one level, listed below; per-application levels will follow once that metadata exists.

## Stability levels

| Level | Can change | Notice before removal | Migration |
| --- | --- | --- | --- |
| Alpha | In any minor release | Listed in release notes of the release that changes it | Required whenever existing objects would break |
| Beta | Only after deprecation | At least one minor release and 3 months, whichever is longer | Required |
| Stable | Only after deprecation, removal only in a major release | At least two minor releases and 6 months, whichever is longer | Required |

Rules common to all levels:

- Patch releases (`vX.Y.Z`, `Z > 0`) never contain breaking changes. They are bugfix-only by construction: fixes reach a release line only through backports.
- A breaking change to an existing object always ships with an automatic migration that converts it, or freezes it on its current artifact when there is no successor, as described in [deprecating-applications.md](deprecating-applications.md). Instances are never silently orphaned, at any level, alpha included. This goes beyond the Kubernetes alpha contract on purpose: Cozystack runs production workloads behind alpha API versions today.
- Every breaking change and every deprecation is listed in a dedicated `Breaking changes` / `Deprecations` section of the release notes, with the replacement and the earliest release in which removal can happen.

## Current levels

| Surface | Level |
| --- | --- |
| `apps.cozystack.io/v1alpha1` (managed applications) | Alpha |
| `backups.cozystack.io`, `strategy.backups.cozystack.io` | Alpha |
| `operator.cozystack.io/v1alpha2` | Alpha |
| `gateway.cozystack.io`, `sdn.cozystack.io`, `core.cozystack.io` | Alpha |
| Application parameters | Alpha |
| Platform configuration | Alpha |

The API version string and the level move together for Kubernetes APIs: a group is beta when it is served as `v1betaN`, stable when served as `v1`. Promotion serves the new version alongside the old one, with conversion, for the full notice period of the old level.

## Graduation criteria

Alpha to beta:

- No breaking change in the last two minor releases.
- Upgrade E2E covers objects of this surface created on the previous minor release.
- Used in production by at least two adopters.

Beta to stable:

- No breaking change in the last three minor releases.
- Conversion and migration paths covered by E2E.
- Used in production by at least three adopters from different organizations.

Promotion is proposed in a pull request that updates the table above and is approved by maintainers under the normal [GOVERNANCE.md](../GOVERNANCE.md) rules.

## Supported upgrades

- Any patch release to any later patch release of the same minor line.
- The latest patch of minor `X.Y` to any patch of `X.(Y+1)`. Release E2E currently tests fresh installs only; automated upgrade E2E from the previous minor is planned and is a prerequisite for promoting any surface to beta.
- Skipping minor releases is not supported. Migrations run sequentially and often work across several minors, but that path is not tested.
- Major upgrades follow a dedicated guide, as the v0.41 to v1.0 upgrade did.

## Maintained release lines

Which release lines receive fixes is defined in [SECURITY.md](../SECURITY.md#supported-versions) and is not restated here. Upgrades into any supported line follow the rules above.

## Major releases

A major release may remove deprecated surfaces at any level and changes this policy only through a published compatibility contract. The [roadmap](../ROADMAP.md) plans Cozystack 2.0 to publish one together with its API revision.
