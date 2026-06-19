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
│   └── e2e-apps/         # End-to-end application tests
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
make unit-tests     # Run all unit tests (Helm, BATS, Go, etc.)
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
- `LOAD=1 PUSH=0` — load images locally instead of pushing.

### Values schema generation

Package `values.yaml` files carry annotations (`@param`, `@typedef`, `@field`, `@enum`, `@section`) that `cozyvalues-gen` turns into `values.schema.json` and `README.md`. Run `make generate` in the package after editing annotations. A pre-commit hook runs `make generate` across `packages/apps/*` and `packages/extra/*` on every commit to keep these in sync — see [`contributing.md`](./contributing.md) for the regenerate-before-commit rule.

## Testing

- **Helm unit tests:** `make helm-unit-tests` (runs `hack/helm-unit-tests.sh` over every package that defines a `test` target). `make unit-tests` runs the full unit suite — Helm, BATS, Go, and the preset/readiness checks.
- **E2E tests:** BATS suites in `hack/e2e-apps/` (one `.bats` per app), run through `hack/cozytest.sh`. Conventions for writing and stabilising them — and the CI that runs them — live in [`e2e-testing.md`](./e2e-testing.md).
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

### Documentation

Documentation is organized as follows:
- `docs/` - General documentation
- `docs/agents/` - Instructions for AI agents
- `docs/changelogs/` - Release changelogs
- Main website: https://github.com/cozystack/website

### Domain references for specific tasks

Before working on these areas, read the relevant doc first:

- **Dashboard resources:** `internal/controller/dashboard/README.md`
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

