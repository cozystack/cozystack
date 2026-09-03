# Cozystack Governance

This document defines the governance structure of the Cozystack community, outlining how members collaborate to achieve shared goals.

## Overview

**Cozystack**, a Cloud Native Computing Foundation (CNCF) project, is committed
to building an open, inclusive, productive, and self-governing open source
community focused on building a high-quality open source PaaS and framework for building clouds.

## Sub-projects

Cozystack is developed across several repositories in the `cozystack` GitHub organisation. They do not all carry the same weight, and the distinction that matters is whether a repository's output reaches the artifact an adopter installs.

**Packaged sub-projects** are built or vendored into the Cozystack distribution: install Cozystack and you run their code. They are governed by this project, released under Apache-2.0, and are in scope for the same licence, security, release and maintainership expectations as the main repository.

| Sub-project | What it contributes to the distribution | Status | Owners |
|---|---|---|---|
| [etcd-operator](https://github.com/cozystack/etcd-operator) | Operator image, CRDs and the `etcd-migrate` tool, for etcd clusters backing tenant control planes | Stable | @lllamnyp, @androndo, @sircthulhu |
| [cozy-proxy](https://github.com/cozystack/cozy-proxy) | kube-proxy addon for 1:1 NAT services, with an NFT backend; chart vendored into the platform | Stable | @kvaps, @mattia-eleuteri |
| [keycloak-kms-proxy](https://github.com/cozystack/keycloak-kms-proxy) | PostgreSQL wire-protocol proxy that encrypts Keycloak PII columns, keeping the key in a KMS outside the database | Stable | @sircthulhu, @kvaps |
| [local-ccm](https://github.com/cozystack/local-ccm) | Cloud-controller manager for bare-metal clusters; chart and image | Stable | @kvaps, @IvanHunters |
| [cozystack-scheduler](https://github.com/cozystack/cozystack-scheduler) | kube-scheduler extension with SchedulingClass-aware placement; also a compiled dependency of the main repository | Stable | @lllamnyp |
| [ingress-nginx-with-protobuf-exporter](https://github.com/cozystack/ingress-nginx-with-protobuf-exporter) | Ingress-NGINX controller build carrying the protobuf metrics exporter | Stable | @kvaps |

The Cozystack maintainers are maintainers of every sub-project by default (see [MAINTAINERS.md](./MAINTAINERS.md)). The owners named above are the people whose review is requested on changes in each repository; the same lists are recorded in each repository's `CODEOWNERS`.

**Independent tooling** is governed by this project but is not part of the distribution — an adopter installs Cozystack without it, and chooses these separately: [talm](https://github.com/cozystack/talm) (managing Talos Linux the GitOps way), [cozyhr](https://github.com/cozystack/cozyhr) (a Helm and Flux CD wrapper for local development), [cozyvalues-gen](https://github.com/cozystack/cozyvalues-gen) (schema and README generation for Helm charts), [terraform-provider-cozystack](https://github.com/cozystack/terraform-provider-cozystack), [ansible-cozystack](https://github.com/cozystack/ansible-cozystack), [boot-to-talos](https://github.com/cozystack/boot-to-talos), [talos-bootstrap](https://github.com/cozystack/talos-bootstrap), [talos-meta-tool](https://github.com/cozystack/talos-meta-tool) and [blockstor](https://github.com/cozystack/blockstor) (a software-defined storage system, developed independently of the platform).

**Project infrastructure** carries no shipped code: [website](https://github.com/cozystack/website), [community](https://github.com/cozystack/community) (design proposals and meeting notes), [external-apps-example](https://github.com/cozystack/external-apps-example) (the reference third-party application catalogues are built from), [ccp](https://github.com/cozystack/ccp), [.github](https://github.com/cozystack/.github) and [.project](https://github.com/cozystack/.project) (CNCF project metadata).

**Forks of upstream projects** are held for contributing changes back, or were held for that purpose and have since been archived. One is load-bearing and is called out below; the rest are not built into the distribution, which takes those components from their upstreams directly.

Three things about this organisation are worth stating plainly rather than leaving to be discovered.

**One fork is compiled into the product.** [cozystack/apimachinery](https://github.com/cozystack/apimachinery) is a fork of `kubernetes/apimachinery` carrying a single 34-line patch to `pkg/runtime/scheme.go` on top of the released `v0.35.0` tree, applied through a `replace` directive in `go.mod`. The same fix is proposed upstream as [kubernetes/kubernetes#135537](https://github.com/kubernetes/kubernetes/pull/135537); the fork exists only until that merges, and is pinned to a branch tracking the upstream release rather than to the fork's own trunk.

**Four repositories are private**, and none of them contributes to the distribution: `security-scanner` (automated CVE monitoring, private because it holds pre-disclosure vulnerability state), `infrastructure` (the organisation's own CI infrastructure-as-code, private because it holds cloud credentials and account topology), one GitHub-created security-advisory workspace, and `talos-preboot-iso`, an archived prototype. Nothing an adopter installs is built from a repository they cannot read.

**Telemetry is received by a component adopters do not install.** [cozystack-telemetry-server](https://github.com/cozystack/cozystack-telemetry-server) runs as project infrastructure, but clusters report to it by default, so it is named here rather than omitted as out-of-scope. What is collected, why, and how to turn it off is documented on the [Telemetry](https://cozystack.io/docs/operations/configuration/telemetry/) page.

## Third-party Dependencies

Cozystack packages and ships software it does not govern. Such components remain
under their upstream owner's namespace, are vendored by digest, and are neither
released by this project nor mirrored under `ghcr.io/cozystack`.

They are enumerated, with their licences, on the
[Licenses](https://cozystack.io/docs/v1.6/operations/configuration/licenses/)
page. That page lists upstreams only: what this project maintains is Apache-2.0
and is not listed there individually.

## Community Roles

* **Users:** Members that engage with the Cozystack community via any medium, including Slack, Telegram, GitHub, and mailing lists.
* **Contributors:** Members contributing to the projects by contributing and reviewing code, writing documentation,
  responding to issues, participating in proposal discussions, and so on.
* **Directors:** Non-technical project leaders.
* **Maintainers**: Technical project leaders.

## Contributors

Cozystack is for everyone. Anyone can become a Cozystack contributor simply by
contributing to the project, whether through code, documentation, blog posts,
community management, or other means.
As with all Cozystack community members, contributors are expected to follow the
[Cozystack Code of Conduct](https://github.com/cozystack/cozystack/blob/main/CODE_OF_CONDUCT.md).

All contributions to Cozystack code, documentation, or other components in the
Cozystack GitHub organisation must follow the 
[contributing guidelines](https://github.com/cozystack/cozystack/blob/main/CONTRIBUTING.md).
Whether these contributions are merged into the project is the prerogative of the maintainers.

## Directors

Directors are responsible for non-technical leadership functions within the project.
This includes representing Cozystack and its maintainers to the community, to the press, 
and to the outside world; interfacing with CNCF and other governance entities;
and participating in project decision-making processes when appropriate.

Directors are elected by a majority vote of the maintainers.

## Maintainers

Maintainers have the right to merge code into the project.
Anyone can become a Cozystack maintainer (see "Becoming a maintainer" below).

### Expectations

Cozystack maintainers are expected to:

* Review pull requests, triage issues, and fix bugs in their areas of
  expertise, ensuring that all changes go through the project's code review
  and integration processes.
* Monitor cncf-cozystack-* emails, the Cozystack Slack channels in Kubernetes
  and CNCF Slack workspaces, Telegram groups, and help out when possible.
* Rapidly respond to any time-sensitive security release processes.
* Attend Cozystack community meetings.

If a maintainer is no longer interested in or cannot perform the duties
listed above, they should move themselves to emeritus status.
If necessary, this can also occur through the decision-making process outlined below.

### Becoming a Maintainer

Anyone can become a Cozystack maintainer. Maintainers should be extremely
proficient in cloud native technologies and/or Go; have relevant domain expertise; 
have the time and ability to meet the maintainer's expectations above; 
and demonstrate the ability to work with the existing maintainers and project processes.

To become a maintainer, start by expressing interest to existing maintainers.
Existing maintainers will then ask you to demonstrate the qualifications above
by contributing PRs, doing code reviews, and other such tasks under their guidance.
After several months of working together, maintainers will decide whether to grant maintainer status.

## Project Decision-making Process

Ideally, all project decisions are resolved by consensus of maintainers and directors.
If this is not possible, a vote will be called.
The voting process is a simple majority in which each maintainer and director receives one vote.
