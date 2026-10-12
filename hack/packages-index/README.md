# Cozystack community package index (template)

This directory is a **template** for the standalone `cozystack/packages-index` repository. Copy its contents into that repository's root (moving `github-workflows/validate.yaml` to `.github/workflows/validate.yaml`) to stand up the community index and its publication gate.

The index is metadata only: it records where community packages live and who owns them. Artifacts stay in ordinary OCI registries; nothing is hosted here.

## Layout

```text
entries/<name>.yaml             # one metadata-only entry per package
scripts/validate-entry.sh       # offline gate: cozypkg validate + mandatory cosign
github-workflows/validate.yaml  # CI that runs the gate on every entry change
```

## Entry format

Each file under `entries/` is a single record (see `entries/example.foo.yaml`):

```yaml
name: foo.bar
ociRef: oci://ghcr.io/foo/bar
version: v1.2.3
description: A SQL database operator
maintainer: Foo <foo@example.com>
homepage: https://example.com/foo
tags:
  - database
  - sql
signing:
  identity: https://github.com/foo/bar/.github/workflows/release.yaml@refs/heads/main
  issuer: https://token.actions.githubusercontent.com
```

## Two-lane merge policy

Following krew-index, submissions take one of two lanes:

- **Owner version bump.** A change that only updates an existing entry's `version` or description, leaving `ociRef` and `signing` untouched, auto-merges once the new artifact validates and is signed by the entry's **recorded** cosign identity. This keeps the routine "new release of a listed package" path fast.
- **New entry, or a change to `ociRef`, `maintainer`, or `signing`.** These are the security-relevant edits and require maintainer review before merge.

Cosign verification is **mandatory** on the gate for both lanes: the trust anchor is the recorded signing identity, so a bump fails if the new artifact is not signed by the originally approved identity.

## Trust model

Signature verification happens here, at publication time, not at tap time. `cozypkg tap` and the dashboard connect flow validate an artifact's structure but do not verify its cosign signature: a cluster that taps a listed repository trusts that this index already verified it. The verification points are this CI gate (which pins each release to the entry's recorded identity) and, optionally, Flux `OCIRepository` verification at pull time. Tapping a third-party repository runs its charts in the management cluster, so operators should tap only sources they trust or that appear in a curated index like this one.
