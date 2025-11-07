# AGENTS.md

This file provides structured guidance for AI coding assistants and agents
working with the **Cozystack** project.

## Project Overview

Cozystack is an open-source Kubernetes-based platform and framework for building cloud infrastructure. It provides:

- **Managed Services**: Databases, VMs, Kubernetes clusters, object storage, and more
- **Multi-tenancy**: Full isolation and self-service for tenants
- **GitOps-driven**: FluxCD-based continuous delivery
- **Modular Architecture**: Extensible with custom packages and services
- **Developer Experience**: Simplified local development with cozypkg tool

The platform exposes infrastructure services via the Kubernetes API with ready-made configs, built-in monitoring, and alerts.

## Code Layout

```
.
├── packages/               # Main directory for cozystack packages
│   ├── core/              # Core platform logic charts (installer, platform)
│   ├── system/            # System charts (CSI, CNI, operators, etc.)
│   ├── apps/              # User-facing charts shown in dashboard catalog
│   └── extra/             # Tenant-specific applications
├── dashboards/            # Grafana dashboards for monitoring
├── hack/                  # Helper scripts for local development
│   └── e2e-apps/         # End-to-end application tests
├── scripts/               # Scripts used by cozystack container
│   └── migrations/       # Version migration scripts
├── docs/                  # Documentation
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

### Package Structure

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

## Conventions

### Helm Charts
- Follow **umbrella chart** pattern for system components
- Include upstream charts in `charts/` directory (vendored, not referenced)
- Override configuration in root `values.yaml`
- Use `values.schema.json` for input validation and dashboard UI rendering

### Go Code
- Follow standard **Go conventions** and idioms
- Use **controller-runtime** patterns for Kubernetes controllers
- Namespaces follow pattern: `github.com/cozystack/cozystack/<path>`
- Add proper error handling and structured logging
- Use `declare(strict_types=1)` equivalent (Go's type safety)

### Git Commits
- Use format: `[component] Description`
- Reference PR numbers when available
- Keep commits atomic and focused
- Follow conventional commit format for changelogs

### Documentation
- Keep README files current
- Document breaking changes clearly
- Update relevant docs when making changes
- Use clear, concise language with code examples

## Development Workflow

### Standard Make Targets

Every package includes a `Makefile` with these targets:

```bash
make update  # Update Helm chart and versions from upstream
make image   # Build Docker images used in the package
make show    # Show rendered Helm templates
make diff    # Diff Helm release against live cluster objects
make apply   # Apply Helm release to Kubernetes cluster
```

### Using cozypkg

The `cozypkg` tool wraps Helm and Flux for local development:

```bash
cozypkg show       # Render manifests (helm template)
cozypkg diff       # Show live vs desired manifests
cozypkg apply      # Upgrade/install HelmRelease and sync
cozypkg suspend    # Suspend Flux reconciliation
cozypkg resume     # Resume Flux reconciliation
cozypkg get        # Get HelmRelease resources
cozypkg list       # List all HelmReleases
cozypkg delete     # Uninstall release
cozypkg reconcile  # Trigger Flux reconciliation
```

### Example: Updating a Component

```bash
cd packages/system/cilium    # Navigate to package
make update                  # Pull latest upstream
make image                   # Build images
git diff .                   # Review manifest changes
make diff                    # Compare with cluster
make apply                   # Deploy to cluster
kubectl get pod -n cozy-cilium  # Verify deployment
git commit -m "[cilium] Update to vX.Y.Z"
```

## Adding New Packages

### For System Components (operators, CNI, CSI, etc.)

1. Create directory: `packages/system/<component-name>/`
2. Create `Chart.yaml` with component metadata
3. Add upstream chart to `charts/` directory
4. Create `values.yaml` with overrides
5. Generate `values.schema.json` using `readme-generator`
6. Add `Makefile` using `scripts/package.mk`
7. Create `images/` directory if custom images needed
8. Add to bundle configuration in `packages/core/platform/`
9. Write tests in `hack/e2e-apps/`
10. Update documentation

### For User Applications (apps catalog)

1. Create directory: `packages/apps/<app-name>/`
2. Define minimal user-facing parameters in `values.schema.json`
3. Use Cozystack API for high-level resources
4. Add `templates/dashboard-resourcemap.yaml` for UI display
5. Keep business logic in system operators, not in app charts
6. Test deployment through dashboard
7. Document usage in README

### For Extra/Tenant Applications

1. Create in `packages/extra/<app-name>/`
2. Follow same structure as apps
3. Not shown in catalog
4. Installable only as tenant component
5. One application type per tenant namespace

## Tests and CI

### Local Testing
- **Unit tests**: Go tests in `*_test.go` files
- **Integration tests**: BATS scripts in `hack/e2e-apps/`
- **E2E tests**: Full platform tests via `hack/e2e.sh`

### Running E2E Tests

```bash
cd packages/core/testing
make apply    # Create testing sandbox in cluster
make test     # Run end-to-end tests
make delete   # Remove testing sandbox

# Or locally with QEMU VMs:
./hack/e2e.sh
```

### CI Pipeline
- Automated tests run on every PR
- Image builds for changed packages
- Manifest diff generation
- E2E tests on full platform
- Release packaging and publishing

### Testing Environment Commands

```bash
make exec     # Interactive shell in sandbox
make login    # Download kubeconfig (requires mirrord)
make proxy    # Enable SOCKS5 proxy (requires mirrord + gost)
```

## Things Agents Should Not Do

### Never Edit These
- Do not modify files in `/vendor/` (Go dependencies)
- Do not edit generated files: `zz_generated.*.go`
- Do not change `go.mod`/`go.sum` manually (use `go get`)
- Do not edit upstream charts in `packages/*/charts/` directly (use patches)
- Do not modify image digests in `values.yaml` (generated by build)

### Version Control
- Do not commit built artifacts from `packages/*/build/`
- Do not commit generated dashboards
- Do not commit test artifacts or temporary files

### Git Operations
- Do not force push to main/master
- Do not skip hooks (--no-verify, --no-gpg-sign)
- Do not update git config
- Do not perform destructive operations without explicit request

### Changelogs
- Do not manually edit `docs/changelogs/*.md` outside of changelog workflow
- Follow changelog agent rules in `.cursor/changelog-agent.md`
- Use structured format from templates

### Core Components
- Do not modify `packages/core/installer/installer.sh` without understanding migration impact
- Do not change `packages/core/platform/` logic without testing full bootstrap
- Do not alter FluxCD configurations without considering reconciliation loops

## Special Workflows

### Changelog Generation

When working with changelogs (see `.cursor/changelog-agent.md` for details):

1. **Activation**: Automatic when user mentions "changelog" or works in `docs/changelogs/`
2. **Commands**:
   - "Create changelog for vX.Y.Z" → Generate from git history
   - "Review changelog vX.Y.Z" → Analyze quality
   - "Update changelog with PR #XXXX" → Add entry
3. **Process**:
   - Extract version and range
   - Run git log between versions
   - Categorize by BMAD framework
   - Generate structured output
   - Validate against checklist
4. **Templates**: Use `patch-template.md` or `template.md`

### Building Cozystack Container

```bash
cd packages/core/installer
make image-cozystack    # Build cozystack image
make apply              # Apply to cluster
kubectl get pod -n cozy-system
kubectl get hr -A       # Check HelmRelease objects
```

### Building with Custom Registry

```bash
export REGISTRY=my-registry.example.com/cozystack
cd packages/system/component-name
make image
make apply
```

## Buildx Configuration

Install and configure Docker buildx for multi-arch builds:

```bash
# Kubernetes driver (build in cluster)
docker buildx create \
  --bootstrap \
  --name=buildkit \
  --driver=kubernetes \
  --driver-opt=namespace=tenant-kvaps,replicas=2 \
  --platform=linux/amd64 \
  --platform=linux/arm64 \
  --use

# Or use local Docker (omit --driver* options)
docker buildx create --bootstrap --name=local --use
```

## References

- [Cozystack Documentation](https://cozystack.io/docs/)
- [Developer Guide](https://cozystack.io/docs/development/)
- [GitHub Repository](https://github.com/cozystack/cozystack)
- [Helm Documentation](https://helm.sh/docs/)
- [FluxCD Documentation](https://fluxcd.io/flux/)
- [cozypkg Tool](https://github.com/cozystack/cozypkg)
- [Kubernetes Operator Patterns](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)
- [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)

## Community

- [Telegram](https://t.me/cozystack)
- [Slack](https://kubernetes.slack.com/archives/C06L3CPRVN1)
- [Community Calendar](https://calendar.google.com/calendar?cid=ZTQzZDIxZTVjOWI0NWE5NWYyOGM1ZDY0OWMyY2IxZTFmNDMzZTJlNjUzYjU2ZGJiZGE3NGNhMzA2ZjBkMGY2OEBncm91cC5jYWxlbmRhci5nb29nbGUuY29t)

---

## Machine-Readable Summary

```yaml
project: Cozystack
type: kubernetes-platform
description: Open-source platform for building cloud infrastructure
architecture: kubernetes-based, gitops-driven, multi-tenant

layout:
  packages/:
    core/: platform bootstrap and configuration
    system/: cluster-wide components (CSI, CNI, operators)
    apps/: user-facing applications (catalog)
    extra/: tenant-specific applications
  dashboards/: grafana monitoring dashboards
  hack/: development scripts and e2e tests
  scripts/: runtime scripts and migrations
  cmd/: go command entry points
  internal/: internal go packages
  pkg/: public go packages
  api/: kubernetes api definitions (CRDs)
  docs/: documentation and changelogs

package_structure:
  Chart.yaml: helm chart definition
  Makefile: development workflow targets
  charts/: vendored upstream charts
  images/: docker image sources
  patches/: upstream chart patches
  templates/: additional manifests
  values.yaml: configuration overrides
  values.schema.json: validation schema and UI hints

workflow:
  development_tool: cozypkg
  commands:
    - update: pull upstream charts
    - image: build docker images
    - show: render manifests
    - diff: compare with cluster
    - apply: deploy to cluster
  gitops_engine: FluxCD
  package_manager: Helm

conventions:
  helm:
    pattern: umbrella chart
    upstream: vendored in charts/
    overrides: root values.yaml
  go:
    style: standard go conventions
    framework: controller-runtime
    namespace: github.com/cozystack/cozystack
  git:
    commit_format: "[component] Description"
    reference_prs: true
    atomic_commits: true

testing:
  unit: go test
  integration: bats scripts (hack/e2e-apps/)
  e2e: hack/e2e.sh
  sandbox:
    location: packages/core/testing
    commands: [apply, test, delete, exec, login, proxy]

ci:
  triggers: every PR
  checks:
    - automated tests
    - image builds
    - manifest diffs
    - e2e tests
    - packaging

special_agents:
  changelog:
    activation:
      - files in docs/changelogs/
      - user mentions "changelog"
      - changelog-related requests
    config_file: .cursor/changelog-agent.md
    templates:
      - docs/changelogs/patch-template.md
      - docs/changelogs/template.md
    framework: BMAD categorization

do_not_edit:
  - vendor/
  - zz_generated.*.go
  - packages/*/charts/* (use patches)
  - go.mod manually
  - go.sum manually
  - image digests in values.yaml
  - built artifacts

tools:
  required:
    - kubectl
    - helm
    - docker buildx
    - make
    - go
  recommended:
    - cozypkg
    - mirrord
    - gost
    - readme-generator

core_components:
  bootstrap:
    - packages/core/installer (installer.sh, assets server)
    - packages/core/platform (flux config, reconciliation)
  api:
    - cmd/cozystack-api (api server)
    - cmd/cozystack-controller (main controller)
    - api/v1alpha1 (CRD definitions)
  delivery:
    - FluxCD Helm Controller
    - HelmRelease custom resources

bundle_system:
  definition: packages/core/platform/
  components_from: packages/system/
  user_applications: packages/apps/ + packages/extra/
  tenant_isolation: namespace-based
  one_app_type_per_tenant: true

image_management:
  location: packages/*/images/
  build: make image
  injection: automatic to values.yaml
  format: path + digest
  registry: configurable via REGISTRY env var

multi_arch:
  tool: docker buildx
  platforms: [linux/amd64, linux/arm64]
  driver_options: [kubernetes, docker]
```

