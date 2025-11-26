# AI Agents Overview

This file provides structured guidance for AI coding assistants and agents
working with the **Cozystack** project.

## Agent Documentation

| Agent | Purpose |
|-------|---------|
| [overview.md](./docs/agents/overview.md) | Project structure and conventions |
| [contributing.md](./docs/agents/contributing.md) | Commits, pull requests, and git workflow |
| [changelog.md](./docs/agents/changelog.md) | Changelog generation instructions |
| [releasing.md](./docs/agents/releasing.md) | Release process and workflow |

## Project Overview

**Cozystack** is a Kubernetes-based platform for building cloud infrastructure with managed services (databases, VMs, K8s clusters), multi-tenancy, and GitOps delivery.

## Quick Reference

### Code Structure
- `packages/core/` - Core platform charts (installer, platform)
- `packages/system/` - System components (CSI, CNI, operators)
- `packages/apps/` - User-facing applications in catalog
- `packages/extra/` - Tenant-specific modules
- `cmd/`, `internal/`, `pkg/` - Go code
- `api/` - Kubernetes CRDs

### Conventions
- **Helm Charts**: Umbrella pattern, vendored upstream charts in `charts/`
- **Go Code**: Controller-runtime patterns, kubebuilder style
- **Git Commits**: `[component] Description` format with `--signoff`

### What NOT to Do
- ❌ Edit `/vendor/`, `zz_generated.*.go`, upstream charts directly
- ❌ Modify `go.mod`/`go.sum` manually (use `go get`)
- ❌ Force push to main/master
- ❌ Commit built artifacts from `_out`
