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
digest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
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

`digest` is optional but recommended: when set to the `sha256:<hex>` the gate verified, tapping the entry's short name pins that exact content instead of following the `version` tag, so a later retag of the same version cannot change what installs. The gate records it from the artifact it just verified, and `version` is kept informational alongside it.

## Two-lane merge policy

Following krew-index, submissions take one of two lanes:

- **Owner version bump.** A change that only updates an existing entry's `version`, `digest`, or description, leaving `ociRef` and `signing` untouched, auto-merges once the new artifact validates and is signed by the entry's **recorded** cosign identity. This keeps the routine "new release of a listed package" path fast.
- **New entry, or a change to `ociRef`, `maintainer`, or `signing`.** These are the security-relevant edits and require maintainer review before merge.

Cosign verification is **mandatory** on the gate for both lanes: the trust anchor is the recorded signing identity, so a bump fails if the new artifact is not signed by the originally approved identity.

## Trust model

Signature verification happens here, at publication time, not at tap time. `cozypkg tap` and the dashboard connect flow validate an artifact's structure; they do not verify a cosign signature unless asked (with `--certificate-identity` / `--certificate-oidc-issuer`, which set a Flux pull-time verify policy). A cluster that taps a listed repository otherwise trusts that this index already verified it. The verification point is this CI gate, which pins each release to the entry's recorded identity. Independently of signing, a tapped repository is pinned to a digest and does not follow a moved tag: a `digest` recorded on the entry pins the exact content the gate verified, and a tag-only tap is pinned to the digest it first resolves to. Tapping a third-party repository runs its charts in the management cluster, so operators should tap only sources they trust or that appear in a curated index like this one.
