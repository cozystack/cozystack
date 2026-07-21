# Image references: where they live and what their tags mean

This is the contract for every container image reference cozystack vendors. Read it before changing how an image is built, stamped, promoted, mirrored, or referenced from a chart, and before writing any tool that reads image references out of the tree. The rules below are not descriptive of what the scripts happen to do today — they are the invariants those scripts are required to hold, and each has been broken at least once in a way that shipped.

## The two things a reference carries

Every first-party reference is a **tag plus a digest**: `ghcr.io/cozystack/cozystack/grafana:v1.6.0@sha256:e96e95…`.

The digest decides what actually runs. It is content-addressed and immutable, so a reference carrying one is reproducible no matter what happens to the tag afterwards. This is why a tag that reads `latest@sha256:…` is not a floating reference — the digest wins, and tag movement cannot change the bytes pulled.

The tag decides what humans and tooling *believe* is running, and it is not decoration. A release whose images are tagged `v1.6.0-rc.4` is indistinguishable, to anyone auditing it, from a release candidate. Tooling that constructs `<repo>:<version>` by convention gets a 404 against a tag that was never created. So an inaccurate tag is a real defect even though the digest still resolves — it just fails later, and quietly, which is worse.

**Both must be correct. Neither is allowed to be approximate.**

## Three classes of tag

Which tag an image carries depends on what versions it. Confusing these classes is what makes a correct promotion look broken and a broken one look correct.

| Class | Tag shape | Example | Moves at release? |
| --- | --- | --- | --- |
| **Version-line** | the cozystack version | `grafana:v1.6.0` | **Yes** — every release |
| **Component-versioned** | upstream version + cozystack suffix | `cluster-api-control-plane-provider-kamaji:v0.19.0-cozystack.0` | No |
| **Third-party pass-through** | whatever upstream publishes | `docker.io/library/busybox:1.37.0` | No |

Version-line images are first-party builds whose version *is* the cozystack version; they are the ones a release rewrites and retags. Component-versioned images are first-party rebuilds of an upstream component, versioned by that component so the provenance stays legible — kamaji keeping `v0.19.0-cozystack.0` across a cozystack release is correct, not a missed rewrite. Third-party images are vendored by digest from registries cozystack cannot push to; they are never retagged and never mirrored.

Only class 1 is touched by a promotion. A tool that "fixes" a class 2 or 3 tag to the cozystack version is introducing a bug, not removing one.

## Three storage shapes

A reference can be stored in three different kinds of file, and **all three are first-class**. Every consumer must read all three. The single most repeated defect in this area is a tool that knows about one shape and silently ignores another; it does not fail, it just quietly skips images, and the gap surfaces a release later.

1. **`packages/<group>/<pkg>/values.yaml`** — the common case, in five YAML sub-shapes (single string, split map with `digest`, split map with the digest inside `tag`, chart-global `global.registry.address` + `global.images[]`, and the OCI-artifact `platformSourceUrl`/`platformSourceRef` pair). The sub-shapes exist because vendored upstream charts choose their own values layout; the list is empirical, not closed.
2. **`packages/<group>/<pkg>/images/<name>.tag`** — a plain file holding one reference, read by templates via `.Files.Get` rather than through values. Easy to forget precisely because it never appears in a values file; otherwise an ordinary first-party reference that carries the cozystack version like any other.
3. **A declared file that embeds a reference in surrounding text** — used when a package vendors an upstream manifest verbatim and its `image:` target `sed`s the built reference straight into it. `packages/system/multus/templates/multus-daemonset-thick.yml` is the only instance today.

`hack/lib/image-refs.sh` is the single enumeration of all three, and the only place that knowledge is allowed to live. `image_ref_files <root>` yields the files; `collect_image_refs <root>` yields the references inside them. Shape 3 is an explicit list (`IMAGE_REF_EXTRA_FILES`) rather than a `templates/**` glob, because such a glob would sweep in vendored upstream chart templates and Helm-templated `image:` lines where a blind rewrite corrupts a value that `make update` regenerates.

### Adding a reference in a new location

If a package stamps a reference somewhere the two globs do not cover, add that file to `IMAGE_REF_EXTRA_FILES` in `hack/lib/image-refs.sh` — and nowhere else. Every consumer picks it up at once. Do not teach an individual script about a new path; that is how the shapes diverged in the first place.

## Invariants

- **Every first-party reference is tag+digest.** A bare tag with no digest is not reproducible and must not be committed. `securitygroup-controller:v0.0.0` is a live violation.
- **Exactly one producer stamps each reference.** A reference no `Makefile` writes will never be refreshed by anything, including release preparation — `chBackupClientImage` in `packages/system/backupstrategy-controller/values.yaml` is a live violation, still pinned to `platform-migrations:v1.4.0-rc.2`.
- **A package that stamps a reference is in the root `Makefile` `build:` list.** `build-matrix.sh`, the main build and release preparation all iterate that one list, so a package outside it is never rebuilt by any path and its pin freezes permanently. `flux-plunger`, `keycloak-operator`, `kilo` and `redis-operator` are live violations (tracked in #3143).
- **Every consumer of references reads all three storage shapes**, via `hack/lib/image-refs.sh`.
- **Scan wider than you rewrite.** A rewrite is deliberately narrow to avoid corrupting vendored defaults, so the check that follows it must not share that blind spot. `hack/promote-rewrite-tags.sh` rewrites the enumerated files, then greps the whole tree and fails if any reference to the release candidate survives.

## What each consumer does

| Tool | Reads | Job |
| --- | --- | --- |
| `hack/promote-rewrite-tags.sh` | all three shapes | rewrite `X.Y.Z-rc.N` → `X.Y.Z` in the tree, then fail if any survives |
| `hack/promote-retag.sh` | all three shapes | copy each digest to its stable tag in the registry |
| `hack/nightly-mirror.sh` | all three shapes | copy each digest to the public registry, then rewrite hosts in the same file set |
| `hack/overlay-main-images.sh` | `values.yaml` + `*.tag` | overlay current-main references onto packages a PR did not rebuild |

`hack/overlay-main-images.sh` is listed for completeness but does not source the shared library — it walks the tree itself. A file newly declared in `IMAGE_REF_EXTRA_FILES` does not automatically reach it.

`nightly-mirror.sh` is the one where a missed file is worst. The set it rewrites hosts in must equal the set it mirrored: a file rewritten but not mirrored leaves a dangling reference to an image never pushed, and a file mirrored but not rewritten leaves the published tree pointing at the private build registry.

## Known gaps

These are real, currently unfixed, and predate the shared enumeration. They are recorded here so nobody rediscovers them as surprises.

**The nightly host rewrite cannot reach a split host.** It is a literal `<src-registry>/` substring replace, so it only rewrites a reference whose host sits contiguously in front of the repository. Two live layouts defeat it: `keycloak-operator` splits the host into a sibling `registry:` key, and `kubeovn` puts it in `global.registry.address` with no trailing slash. Both images *are* mirrored — collection understands those shapes — but the published tree keeps pointing at the build registry for them. Fixing it properly means structure-aware rewriting rather than a substring pass. This is a defect in the host dimension; the version dimension the promote rewrite handles is unaffected, because the version always lives in a `tag` key regardless of where the host is.

**A reference inside a gzip is invisible.** `capi-providers-cpprovider` stamps its kamaji reference into `files/control-plane-components.yaml` and into the `files/components.gz` built from it, and the chart ships the `.gz`. Nothing enumerates either: declaring only the readable copy would rewrite what nothing consumes and diverge it from what does. Note the promote postcondition cannot catch this class at all — `grep -rIl` skips binary files — so it fails silently rather than loudly. No tag rewrite is owed (kamaji is component-versioned) and the image is still mirrored and retagged via its `.tag` file, so the residual is that a nightly's kamaji ConfigMap keeps the build-registry host. The fix is decompress, rewrite, recompress with `gzip -n` for reproducibility.

**`charts/` is excluded, and is almost always right.** Vendored upstream chart values are regenerated by `make update` and their images are neither built nor pushed here. The one exception is `packages/system/cozystack-scheduler/charts/cozystack-scheduler/values.yaml`, which pins a first-party-namespace image. Excluding it is still correct — it is built by a separate repository, component-versioned, and already published to the public registry, so no retag or mirror is owed — but "everything under `charts/` is third-party" is not literally true.

## Why promotion does not rebuild

Promotion copies the release candidate's images to the stable tag **by digest**, so the stable release is bit-for-bit what passed end-to-end testing. Nothing is recompiled, and promotion cannot diverge from what was tested. The only thing that changes in the tree is the cosmetic tag string, which is what `hack/promote-rewrite-tags.sh` does — and why getting its file coverage right is the difference between a release that reads as a release and one that reads as a release candidate.

## Testing

`hack/promote-rewrite-tags_test.bats` round-trips the rewrite against the real tree: it stamps a synthetic release-candidate version into every first-party reference, runs the rewrite, and asserts nothing retains it. This is the check that catches a new storage shape, and it runs under `make unit-tests` without a cluster, a registry or a release. Any change to how references are stored must keep it passing.
