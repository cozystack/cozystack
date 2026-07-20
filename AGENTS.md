# AI Agents Overview

This file provides structured guidance for AI coding assistants and agents working with the **Cozystack** project.

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

- **Go code, Helm charts, build and test commands, project structure** (e.g., "add a field to this CRD", "fix this controller", "add a value to the postgres chart", "regenerate the values schema", "how do I build/test this", "where should I put X", "what's the convention for Y", "how is the project organized")
  - Read: [`overview.md`](./docs/agents/overview.md)
  - Action: Read the section that matches — `Go Code` (controller-runtime patterns, kubebuilder markers), `Helm Charts` (umbrella pattern, vendored upstream charts), `Values schema generation`, `Build and Development Commands`, `Testing`, `Package Structure`

- **E2E tests and E2E CI** (e.g., "write/fix an e2e test", "stabilize a flaky test", "add a bats test", "change the e2e workflow", "why did e2e fail")
  - Read: [`e2e-testing.md`](./docs/agents/e2e-testing.md)
  - Action: Read the entire file and follow the conventions exactly — no new retries on deterministic steps, event-driven backstops before every `kubectl wait`, no `EXIT`/`RETURN` traps, fail-fast on HelmRelease readiness

- **General questions about contributing**
  - Read: [`contributing.md`](./docs/agents/contributing.md)
  - Action: Read the file to understand git workflow, commit format, PR process

- **Issue and PR labeling, triage** (e.g., "label this issue", "what label should I use", "triage this", "categorize")
  - Read: [`.github/labels.yml`](./.github/labels.yml)
  - Action: Use labels defined there. Conventions follow the Kubernetes scheme — `kind/*` (type), `area/*` (subsystem), `priority/*` (urgency), `triage/*` (review state), `lifecycle/*` (auto-close), `do-not-merge/*` (PR blockers), `security/*` (severity)
  - For `area/*`: accuracy outweighs reuse. If no existing `area/*` truly fits the change, propose a new one via PR (extend `.github/labels.yml` and the scope mapping in `.github/workflows/pr-labeler.yaml`) — do not shoehorn the change into a wrong area. `area/uncategorized` is the auto-labeler fallback; treat it as a signal to pick a fit, create a new area, or correct the PR title
  - PR titles: a Conventional Commits header (`type(scope): description`, types from [`contributing.md`](./docs/agents/contributing.md)) auto-applies `kind/*` and `area/*` via `.github/workflows/pr-labeler.yaml`. Append `!` (or add a `BREAKING CHANGE:` footer) to apply `kind/breaking-change`

**Important rules:**
- **Match scope, then read first.** Load a process file only when the task genuinely falls in its scope, and load it before starting work — not midway through. Tasks outside a file's scope should not pull it into context.
- **Follow it exactly.** Do not assume you already know the process, and do not skip steps (`changelog.md` especially). Where the documentation and your instinct disagree, the documentation wins.
- **[`overview.md`](./docs/agents/overview.md) is the standing reference** for structure and conventions, useful even when no route above points at it.

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
- **Git Commits**: Conventional Commits (`type(scope): description`) with `--signoff`
- **Prose**: One continuous line per paragraph — see [Prose Formatting](#prose-formatting)

### What NOT to Do
- ❌ Edit `/vendor/`, `zz_generated.*.go`, upstream charts directly
- ❌ Modify `go.mod`/`go.sum` manually (use `go get`)
- ❌ Force push to main/master
- ❌ Commit built artifacts from `_out`
- ❌ Hardwrap a prose paragraph in markdown, a PR body, or an issue comment

## Prose Formatting

Markdown files and everything an agent publishes to GitHub — PR bodies, issue bodies, review comments, release notes — use **one continuous line per paragraph**. Renderers collapse a soft line break into a space, so a paragraph hardwrapped at ~80 columns renders exactly like the same paragraph on one line, while breaking narrow viewports, mangling nested list and table rendering, and turning a one-word edit into a diff that reflows the entire block. The renderer decides where the line ends; the author does not.

Line breaks stay wherever they carry meaning: code fences, one item per list line, tables, headings, blockquotes, front matter, and README badge stacks. The rule governs prose paragraphs and nothing else. Commit messages are the deliberate exception — `git log` and the tooling around it still expect a body wrapped at ~72 columns.

This is the rule LLM agents break most often, because an ~80-column wrap is correct in the places they spend most of their time (Go comments, commit bodies) and the reflex leaks from there into documentation and GitHub. Treat it as a mechanical check at the moment of writing, not as a style preference to weigh: before saving a markdown file or passing `--body` to `gh`, look at each paragraph and confirm it is a single line.

The rule applies to prose this repository owns, and to new or changed paragraphs. Much of the existing tree predates it and is wrapped throughout; that is not a defect to go and fix. Vendored upstream chart docs (`packages/*/*/charts/**`) must stay verbatim — `make update` regenerates them — and published changelogs are historical records. Neither is in scope.

`.claude/hooks/md-no-hardwrap.py` enforces the rule for agents that support PreToolUse hooks (registered for Claude Code in `.claude/settings.json`). It refuses a `Write`/`Edit`/`MultiEdit` that would *add* a hardwrapped paragraph, list item or blockquote to a markdown file, and a `gh` command that would publish one to GitHub. A break counts as added only when the edit wrote both lines it joins, so fixing a typo inside a paragraph that was already wrapped goes through, while rewriting the same file as fresh hardwrapped prose does not. Anything it cannot read with certainty it allows — a guard that blocks legitimate work gets switched off, and then it guards nothing — which includes a body the shell assembles at runtime, and a `--body-file` written by the very command being checked, since the file does not exist yet when the hook runs. `hack/md-no-hardwrap.bats` covers it under `make unit-tests` and requires `python3`. Agents on other harnesses are held to the same rule without the hook.

`.claude/settings.json` is tracked, so keep personal Claude Code overrides in `.claude/settings.local.json`, which is not. A personal `.claude/settings.json` predating this will be overwritten on pull without warning, because git had been ignoring it.
