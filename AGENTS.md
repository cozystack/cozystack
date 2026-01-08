# AI Agents Overview

This file provides structured guidance for AI coding assistants and agents
working with the **Cozystack** project.

## Activation

**CRITICAL**: When the user asks you to do something that matches the scope of a documented process, you MUST read the corresponding documentation file and follow the instructions exactly as written.

- **Commits, PRs, git operations** (e.g., "create a commit", "make a PR", "fix review comments", "rebase", "cherry-pick")
  - Read: [`contributing.md`](./docs/agents/contributing.md)
  - Action: Read the entire file and follow ALL instructions step-by-step

- **Changelog generation** (e.g., "generate changelog", "create changelog", "prepare changelog for version X")
  - Read: [`changelog.md`](./docs/agents/changelog.md)
  - Action: Read the entire file and follow ALL steps in the checklist. Do NOT skip any mandatory steps

- **Release creation** (e.g., "create release", "prepare release", "tag release", "make a release")
  - Read: [`releasing.md`](./docs/agents/releasing.md)
  - Action: Read the file and follow the referenced release process in `docs/release.md`

- **Project structure, conventions, code layout** (e.g., "where should I put X", "what's the convention for Y", "how is the project organized")
  - Read: [`overview.md`](./docs/agents/overview.md)
  - Action: Read relevant sections to understand project structure and conventions

- **General questions about contributing**
  - Read: [`contributing.md`](./docs/agents/contributing.md)
  - Action: Read the file to understand git workflow, commit format, PR process

**Important rules:**
- ✅ **ONLY read the file if the task matches the documented process scope** - do not read files for tasks that don't match their purpose
- ✅ **ALWAYS read the file FIRST** before starting the task (when applicable)
- ✅ **Follow instructions EXACTLY** as written in the documentation
- ✅ **Do NOT skip mandatory steps** (especially in changelog.md)
- ✅ **Do NOT assume** you know the process - always check the documentation when the task matches
- ❌ **Do NOT read files** for tasks that are outside their documented scope
- 📖 **Note**: [`overview.md`](./docs/agents/overview.md) can be useful as a reference to understand project structure and conventions, even when not explicitly required by the task

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
