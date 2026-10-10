# Cozystack Project Overview

This document provides detailed information about Cozystack project structure and conventions for AI agents.

## About Cozystack

Cozystack is an open-source Kubernetes-based platform and framework for building cloud infrastructure. It provides:

- **Managed Services**: Databases, VMs, Kubernetes clusters, object storage, and more
- **Multi-tenancy**: Full isolation and self-service for tenants
- **GitOps-driven**: FluxCD-based continuous delivery
- **Modular Architecture**: Extensible with custom packages and services
- **Developer Experience**: Simplified local development with cozyhr tool

The platform exposes infrastructure services via the Kubernetes API with ready-made configs, built-in monitoring, and alerts.

## Code Layout

```
.
├── packages/               # Main directory for cozystack packages
│   ├── core/              # Core platform logic charts (installer, platform)
│   ├── system/            # System charts (CSI, CNI, operators, etc.)
│   ├── apps/              # User-facing charts shown in dashboard catalog
│   └── extra/             # Tenant-specific modules, singleton charts which are used as dependencies
├── dashboards/            # Grafana dashboards for monitoring
├── hack/                  # Helper scripts for local development
│   └── e2e-chainsaw/     # End-to-end application tests (Kyverno Chainsaw)
├── scripts/               # Scripts used by cozystack container
│   └── migrations/       # Version migration scripts
├── docs/                  # Documentation
│   ├── agents/           # AI agent instructions
│   └── changelogs/       # Release changelogs
├── cmd/                   # Go command entry points
│   ├── cozystack-api/
│   ├── cozystack-controller/
│   └── cozystack-assets-server/
├── internal/              # Internal Go packages
│   ├── controller/       # Controller implementations
│   └── lineagecontrollerwebhook/
├── pkg/                   # Public Go packages
│   ├── apis/
│   ├── apiserver/
│   └── registry/
└── api/                   # Kubernetes API definitions (CRDs)
    └── v1alpha1/
```

## Package Structure

Every package is a Helm chart following the umbrella chart pattern:

```
packages/<category>/<package-name>/
├── Chart.yaml                           # Chart definition and parameter docs
├── Makefile                             # Development workflow targets
├── charts/                              # Vendored upstream charts
├── images/                              # Dockerfiles and image build context
├── patches/                             # Optional upstream chart patches
├── templates/                           # Additional manifests
├── templates/dashboard-resourcemap.yaml # Dashboard resource mapping
├── values.yaml                          # Override values for upstream
└── values.schema.json                   # JSON schema for validation and UI
```

## Build and Development Commands

Root targets (run from the repo root):

```bash
make build          # Build all Docker images (needs: docker, skopeo, jq, gh, helm, yq, GNU tar/sed/awk)
make unit-tests     # Run unit tests (requires bats-core >= 1.5; GNU parallel is optional)
make test-controllers # Run the Go tests under ./internal/... (controllers, contract tests)
make generate       # Code generation (hack/update-codegen.sh) — CRDs, DeepCopy, clients, RBAC
make manifests      # Generate CRD manifests and operator YAML variants
make cozypkg        # Build the cozypkg CLI
make test           # Full E2E tests (requires a cluster)
make prepare-env    # Prepare the E2E test environment (apply + prepare-cluster)
```

Per-package targets (run inside `packages/{apps,system,extra,core}/<name>/`):

```bash
make image          # Build the package image
make show           # Render the Helm template
make apply          # Apply the Helm release to a cluster (needs NAME, NAMESPACE)
make diff           # Diff the template against the cluster
make test           # Package-specific tests
make generate       # Regenerate values.schema.json + README.md from values.yaml
```

Build environment variables:

- `REGISTRY` — Docker registry (default `ghcr.io/cozystack/cozystack`).
- `PUSH=1` / `LOAD=0` — control buildx push/load behaviour.
- `LOAD=1 PUSH=0` — load images locally instead of pushing. Builds for the host architecture only, because the classic docker image store cannot load a multi-platform index.
- `PLATFORM` — the buildx platforms. Defaults to `linux/amd64,linux/arm64` for anything but `LOAD=1`, so a push from either architecture publishes an index both can run. Needs a `docker-container` builder (`BUILDER`) or a docker daemon on the containerd image store, with QEMU registered for the non-native half. Compiling builder stages run natively on `$BUILDPLATFORM` and cross-compile for the target, but every `RUN` step of a stage that runs on the target platform (package installs, `setcap`, install scripts, patch steps) still runs emulated. `packages/core/testing` pins `linux/amd64`, because it carries an x86 KVM sandbox. `packages/core/talos` builds the Talos installer and release assets for the arches in `PLATFORM` with the upstream imager, which needs no QEMU for either; its matchbox image carries the amd64 and arm64 kernel and initramfs whatever `PLATFORM` says, because it network-boots machines of either arch.
- `PUSHED_TAGS_LOG` — a file the `image-tags` macro appends every pushed `<repo>:<tag>` to, one per line. Unset by default; the release build sets it so `hack/stitch-multiarch.sh` can move every tag it pushed, versioned ones included, onto the multi-arch index. The append happens at recipe expansion, so `make -n` writes it too.
- `CACHE_TAG` — the tag of the mode=max build cache each image reads (and writes under `WRITE_CACHE=1`). Defaults to `buildcache`. Apart from the Talos installer and matchbox in the release job, CI builds one architecture per job, and builds arm64 images only for pre-release tags and in a nightly build of main: the images are built again on native arm64 runners under `<tag>-arm64` with `CACHE_TAG=buildcache-arm64`, which only the nightly writes. The talos and testing packages get no arm64 leg: the release job builds the Talos installer and matchbox for both arches itself, and testing is amd64 only. For a pre-release, `hack/stitch-multiarch.sh` then joins each pair into one index, `hack/verify-multiarch.sh` fails the rc on any pinned image that is not an amd64+arm64 index, and the packages artifact is republished pinned on the index digests; see `docs/release.md`. The stitch skips any image whose digest is also pinned in a file it does not rewrite, which then fails that gate; the kamaji control-plane provider's gzipped manifest is the one such copy it does rewrite (see `docs/agents/image-refs.md`).

### Values schema generation

Package `values.yaml` files carry annotations (`@param`, `@typedef`, `@field`, `@enum`, `@section`) that `cozyvalues-gen` turns into `values.schema.json` and `README.md`. Run `make generate` in the package after editing annotations. A pre-commit hook runs `make generate` across `packages/apps/*` and `packages/extra/*` on every commit to keep these in sync — see [`contributing.md`](./contributing.md) for the regenerate-before-commit rule.

A chart also declares the limits on its own resource name there, with `## @name {string} - <why>` followed by the constraint tags (`@maxLength`, `@minLength`, `@pattern`) it needs. `cozyvalues-gen` publishes the declaration at the root of `values.schema.json` as `x-cozystack-name`, from where `hack/update-crd.sh` carries it into the ApplicationDefinition, and the aggregated API server enforces it on `metadata.name` for whatever kind the definition describes. The description is the message a rejected name is answered with, so write it as the explanation of the limit. A `@pattern` is unanchored, as JSON Schema specifies, so one meant for the whole name is written `^...$`. A declaration the server cannot enforce as written — a constraint keyword it does not implement, a wrong type, a null, lengths no name satisfies, a pattern that is not RE2 — refuses every create of that kind with a configuration error rather than being dropped. A pattern is checked only to compile, not to match anything, so one that matches no name (`$^`) rejects every name with the pattern as the reason. Annotation keywords such as `title` or `examples`, and `x-` extensions, are ignored. A cap only ever tightens: `prefix + name` must still fit the 53-character Helm release name limit, and a declaration looser than that budget does not widen it. The limits belong in the chart because that is where the facts behind them are — which sub-resources it renders and what release names they take — and a chart that needs nothing beyond the platform-wide limits declares nothing.

## Testing

- **Helm and BATS unit tests:** `make helm-unit-tests` runs `hack/helm-unit-tests.sh` over every package that defines a `test` target. A `test` target is expected to run helm-unittest: the sweep fails a package whose run reports no suite, because exiting 0 having asserted nothing is indistinguishable from passing. A target that drives something else belongs under another name, as `packages/core/testing` already does with its sandbox flow. `make bats-unit-tests` requires bats-core 1.5 or newer; GNU parallel is optional and the target runs serially without it. Run the complete local BATS and POSIX-compatibility pass with `pre-commit run bats-unit-tests --hook-stage manual --all-files`; pull-request CI runs both lanes for code changes and the BATS contracts for docs-only changes. On a host whose `/bin/sh` differs from CI, `make BATS_POSIX_SHELL=dash bats-posix-compat-tests` selects the same interpreter explicitly. `make unit-tests` runs the full unit suite — Helm, BATS, the targeted POSIX-shell compatibility pass, Go, and the preset/readiness checks. It does not include the controller tests under `./internal/...`; run `make test-controllers` as well. CI invokes both targets together.
- **E2E tests:** Kyverno Chainsaw suites in `hack/e2e-chainsaw/` (one directory per app), run with `chainsaw test`. Cluster bootstrap (`hack/e2e-install-cozystack.bats`) and the OpenAPI checks (`hack/e2e-test-openapi.bats`) remain BATS. Conventions for writing and stabilising them — and the CI that runs them — live in [`e2e-testing.md`](./e2e-testing.md).
- **Go tests:** standard `go test`, with Ginkgo/Gomega for controllers.

## Conventions

### Helm Charts
- Follow **umbrella chart** pattern for system components
- Include upstream charts in `charts/` directory (vendored, not referenced)
- Override configuration in root `values.yaml`
- Use `values.schema.json` for input validation and dashboard UI rendering

### Go Code
- Follow standard **Go conventions** and idioms
- Use **controller-runtime** patterns for Kubernetes controllers
- Prefer **kubebuilder** for API definitions and controllers
- Add proper error handling and structured logging

### Git Commits

Conventional Commits (`type(scope): description`) with `--signoff`, kept atomic and focused. Full format, scopes, AI-attribution trailer, and PR workflow are in [`contributing.md`](./contributing.md) — the source of truth; do not duplicate them here.

### PackageSource CRD upgrade policy

Each component in a `PackageSource` may set `install.upgradeCRDs` to control how CRDs from the chart's `crds/` directory are handled on `HelmRelease` upgrades. Allowed values: `Skip` (default — helm-controller does not touch CRDs on upgrade), `Create` (create new CRDs only), `CreateReplace` (create new and overwrite existing).

Set `upgradeCRDs: CreateReplace` for operators whose upstream regularly adds new CRDs between versions (etcd-operator, cnpg, kubevirt, kamaji). Without it, new CRDs from a chart bump do not land on existing clusters — only fresh installs get them.

Do **not** set `CreateReplace` blindly: it overwrites every CRD in `crds/` and can cause silent data loss if upstream drops a field from a CRD that has live objects. Only enable it for operators whose schema evolution is additive-only. When in doubt, leave it unset and apply new CRDs manually.

### Documentation — where a new document goes

Product documentation lives in [`cozystack/website`](https://github.com/cozystack/website), not here. This repository holds only documentation that something in the repository references by path, or that describes the repository's own machinery. Pick the destination before writing, because moving a document later means chasing every `@param`, template comment and test comment that points at it.

| Destination | What belongs there |
| --- | --- |
| `packages/<group>/<name>/README.md` | Anything scoped to a single package. Prose goes **above** `## Parameters`; that section is `cozyvalues-gen` output and is never hand-edited. The website's `make update-apps` / `update-vms` / `update-networking` / `update-k8s` / `update-services` targets append the whole README onto a published page, so this route reaches users without a second PR. Prefer it whenever the content fits one package. |
| `cozystack/website`, under `content/en/docs/` | Operator- and tenant-facing content that spans packages or is task-shaped: guides, how-tos, troubleshooting runbooks, reference tables. A new page goes into both `next/` and the current released version directory — `next/` alone is excluded from production builds and will not be visible. |
| `docs/` | Documentation that a chart template, migration, script or workflow references by repo-relative path, plus this repository's own process docs (`docs/agents/`, `docs/changelogs/`, `docs/release.md`). |
| `api/<group>/<version>/DESIGN.md`, `pkg/apis/<group>/DESIGN.md` | Design of an API group, beside the types it describes. |
| [`cozystack/community`](https://github.com/cozystack/community) | Design proposals and RFCs. Not this repository. |

When a document could go in either `docs/` or the website, the test is whether a non-documentation file has to link to it by path. `docs/operations/seaweedfs-431-rename-recovery.md` stays because `packages/*/seaweedfs/templates/_naming.tpl` and a platform migration point at it. A guide referenced only from a `values.yaml` `@param` does not qualify — put it on the website and link the URL.

Documenting a feature's current limitations is useful and belongs with the feature. Documenting the *development process* around it does not: no "What is NOT in Phase N", no "out of scope for this PR", no "this PR" phrasing, no action plans or review negotiation. A merged document describes what the code does now, and states a limitation as a limitation rather than as a roadmap position. Scope, phasing and follow-ups belong in the pull request description or an issue.

A document that names an unmerged pull request or an unreleased dependency needs a tracking issue, because nothing re-checks that claim later and it silently becomes false once the thing ships.

### Domain references for specific tasks

Before working on these areas, read the relevant doc first:

- **Backup subsystem design:** `api/backups/v1alpha1/DESIGN.md`
- **A new app package:** copy the structure of an existing one (e.g. `packages/apps/postgres/`)

## Things Agents Should Not Do

### Never Edit These
- Do not modify files in `/vendor/` (Go dependencies)
- Do not edit generated files: `zz_generated.*.go`
- Do not change `go.mod`/`go.sum` manually (use `go get`)
- Do not edit upstream charts in `packages/*/charts/` directly (use patches)
- Do not modify image digests in `values.yaml` (generated by build)

### Version Control
- Do not commit built artifacts from `_out`
- Do not commit test artifacts or temporary files

### Git Operations
- Do not force push to main/master
- Do not update git config
- Do not perform destructive operations without explicit request

### Core Components
- Do not modify `packages/core/platform/` without understanding migration impact
