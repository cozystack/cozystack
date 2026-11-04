# Contributing to Cozystack

Welcome! We are glad that you want to contribute to our Cozystack project! 💖

As you get started, you are in the best position to give us feedbacks on areas of our project that we need help with, including:

* Problems found while setting up the development environment
* Gaps in our documentation
* Bugs in our GitHub actions

First, though, it is important that you read the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/master/code-of-conduct.md).

The guidelines below are a starting point. We don't want to limit your
creativity, passion, and initiative. If you think there's a better way, please
feel free to bring it up in a GitHub discussion, or open a pull request. We're
certain there are always better ways to do things, we just need to start some
constructive dialogue!

## Ways to contribute

We welcome many types of contributions including:

* New features
* Builds, CI/CD
* Bug fixes
* [Documentation](https://GitHub.com/cozystack/cozystack-website/tree/main)
* Issue Triage
* Answering questions on Slack or GitHub Discussions
* Web design
* Communications / Social Media / Blog Posts
* Events participation
* Release management

## Ask for Help

The best way to reach us with a question when contributing is to drop a line in
our [Telegram channel](https://t.me/cozystack), or start a new GitHub discussion.

## Where do I file?

Cozystack work is split across two repositories. Use this table to land in the right place:

| You have… | Where it goes |
|---|---|
| A **bug** | [New issue → Bug report](https://github.com/cozystack/cozystack/issues/new/choose) in this repo (`kind/bug`) |
| A **concrete, scoped feature** | [New issue → Feature request](https://github.com/cozystack/cozystack/issues/new/choose) in this repo (`kind/feature`) |
| A **usage question** or an early idea | [GitHub Discussions](https://github.com/cozystack/cozystack/discussions) |
| A **cross-cutting / architectural change** — affects multiple components or APIs, or needs a decision before code | A [design proposal](https://github.com/cozystack/community/tree/main/design-proposals) in the [**community**](https://github.com/cozystack/community) repo |
| **Governance, process, or community** matters | The [community](https://github.com/cozystack/community) repo |

Rule of thumb: **bugs and concrete features live here; "what or whether we should build it" lives in [community](https://github.com/cozystack/community)** as a design proposal or discussion. A large feature often starts as a Discussion, graduates to a design-proposal PR, then spawns implementation issues here.

## Where new code goes

Cozystack is being split into the **platform** and two **catalogs**, **virtualization** and **services**. The design and the reasons are in the [split design proposal](https://github.com/cozystack/community/tree/main/design-proposals/cozystack-split). Until the split is done this repository holds all three, and the rules below keep the boundary from getting worse in the meantime. Reviewers link to this section when they reject a change under it.

New code follows these rules. Existing code that breaks them is technical debt and gets refactored, it is not a precedent for new code.

### What belongs where

- **platform**: the operator, CRDs, `cozystack-api`, `cozystack-controller`, the dashboard, tenants and tenant modules in `packages/extra/` except `computeplane`, `cozy-lib`, networking with multus as an option, storage, the host GPU stack, cert-manager, monitoring, identity, object storage (SeaweedFS, the COSI controller, the Bucket application), and operators that both catalogs or the platform itself need, such as `postgres-operator`.
- **virtualization**: virtual machines and everything that exists for them: VM disks and images, VPC, GPUs for VMs and for tenant clusters, VM import, tenant Kubernetes (its workers are VMs) and `computeplane`, and the components only they need, such as KubeVirt, CDI, Cluster API and Kamaji.
- **services**: finished software a tenant consumes, such as databases, message queues, caches, the registry, secrets and VPN, and the operators only they need.

A component belongs to virtualization if it exists for VMs or for what runs on VMs, to services if it is finished software a tenant consumes, and to the platform if everything needs it. An operator or controller that serves one application belongs with that application.

### New applications

New platform components are accepted at the maintainers' discretion.

A new application starts as a design proposal in [cozystack/community](https://github.com/cozystack/community/tree/main/design-proposals), and the proposal decides whether the project takes it and into which catalog. If you are not sure, ask the maintainers in any channel or open the proposal as a draft.

The project takes an application into its catalog when all of these hold:

- a maintainer or reviewer from [MAINTAINERS.md](MAINTAINERS.md) takes it in `.github/CODEOWNERS`;
- licenses of the application and its operator are acceptable for a CNCF project;
- it has tests for what the chart adds on top of the upstream operator;
- its upstream operator is maintained;
- users need it, and the proposal shows that.

Accepted application goes into its catalog directory in this repository, together with any operator, controller or CRD that only it uses. When the catalogs move to their own repositories, it moves with them.

Any other application is published by its author as a catalog from author's own repository, see [Publishing your own catalog](#publishing-your-own-catalog). The project does not host such catalogs.

These rules apply to proposals, pull requests and issues that are already open too, there is no grace period.

Changes to applications already in this repository are welcome: bug fixes, version bumps, new options and features.

### No new coupling

Platform code must not learn about a specific application. Platform code is everything under `api/`, `cmd/`, `internal/`, `pkg/`, `packages/core/`, `packages/library/`, the platform modules in `packages/extra/`, the dashboard, and the platform packages in `packages/system/`. A change is rejected when platform code gains any of these for one catalog application:

1. the API group of the application's operator, for example a platform controller that imports `postgresql.cnpg.io` types;
2. the application's kind or plural under `apps.cozystack.io`, for example `postgreses` added to a tenant role in `cozystack-basics`;
3. its identifiers: a `cozystack.<name>` source name, a component path, a `cozy-<name>` namespace or a release name;
4. a `cozy-lib` helper that only it uses;
5. a monitoring dashboard or alert rule written for it.

One more entry in a list that already names applications is still new coupling.

| Rejected | Do this instead |
|---|---|
| Adding an application kind to a tenant role in `cozystack-basics` | Ship a `ClusterRole` from the application's `-rd` chart labelled `rbac.cozystack.io/aggregate-to-tenant-<level>: "true"`, as `backup-controller` does |
| Adding a backup strategy type or a `case` for one application to the backup controller | Use the generic `Job` strategy where it fits, otherwise open an issue first |
| Adding an alert rule for one application to `monitoring` or `monitoring-agents` | Ship it from the application chart as a `PrometheusRule`, as `packages/extra/etcd` does |
| Adding a Grafana dashboard for one application to `monitoring` | Open an issue, an application chart has no way to ship its own dashboard yet |
| Adding an option provider to `pkg/registry/core/option` that reads one application's API or namespace | Open an issue for a declarative option source |
| Hardcoding an icon, category or tab for one kind in the dashboard | Declare it in the `ApplicationDefinition` under `spec.dashboard` (`icon`, `category`, `tabs`, `keysOrder`), and open an issue if the field you need is missing |
| Adding a warning or limit for one application to `cozystack-api` | Ship a `ValidatingAdmissionPolicy` in the application chart, with the `Warn` action for a warning |

When the alternative does not exist yet, the change waits for it or goes through an exception. Don't add the coupling as a stopgap.

### Out of scope

- Vendored upstream charts under `packages/*/*/charts/`. Their content belongs to upstream. A patch in `patches/` that adds coupling is still in scope.
- Moving an existing application out of this repository. That is planned work under the design proposal. Removing an application from the tree breaks running instances, see [docs/deprecating-applications.md](docs/deprecating-applications.md).

### Exceptions

Open an issue in this repository before the pull request. Say which rule blocks the change, why the alternative does not work, and how the coupling gets removed later. Link the issue from the pull request. An exception needs approval from a core maintainer listed in [MAINTAINERS.md](MAINTAINERS.md).

## Publishing your own catalog

A catalog is an OCI artifact in the `PackageSource` layout that `cozypkg init` creates, `cozypkg validate` checks and `cozypkg push` publishes. How a cluster connects it depends on the release:

- Cozystack 1.6: create an `OCIRepository` for the artifact, apply its `PackageSource` objects, then install with `cozypkg add`.
- Cozystack 1.7 and later: connect the artifact through the marketplace with `cozypkg tap` or from the dashboard, then install with `cozypkg add`. See the [marketplace documentation](https://cozystack.io/docs/v1.7/marketplace/).

The older layout from [cozystack/external-apps-example](https://github.com/cozystack/external-apps-example), a Git repository with a bootstrap `HelmRelease`, keeps working, but don't start a new catalog with it.

Give tenant administrators access to your kinds with a `ClusterRole` labelled `rbac.cozystack.io/aggregate-to-tenant-admin: "true"` in your `-rd` chart. The tenant roles in this repository list only the project's own kinds. If your charts use `cozy-lib`, vendor a copy into your catalog until the library is published as a versioned chart.

## Raising Issues

When raising issues, please specify the following:

- A scenario where the issue occurred (with details on how to reproduce it)
- Errors and log messages that are displayed by the involved software
- Any other detail that might be useful
