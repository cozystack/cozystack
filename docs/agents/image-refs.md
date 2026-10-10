# Image references: where they live and what their tags mean

This is the contract for every container image reference cozystack vendors. Read it before changing how an image is built, stamped, promoted, mirrored, or referenced from a chart, and before writing any tool that reads image references out of the tree. The rules below are not descriptive of what the scripts happen to do today — they are the invariants those scripts are required to hold, and each has been broken at least once in a way that shipped.

## The two things a reference carries

Every first-party reference is a **tag plus a digest**: `ghcr.io/cozystack/cozystack/cozystack-api:v1.5.0@sha256:ef50b1…`.

The digest decides what actually runs. It is content-addressed and immutable, so a reference carrying one is reproducible no matter what happens to the tag afterwards. This is why a tag that reads `latest@sha256:…` is not a floating reference — the digest wins, and tag movement cannot change the bytes pulled.

The tag decides what humans and tooling *believe* is running, and it is not decoration. A release whose images are tagged `v1.6.0-rc.4` is indistinguishable, to anyone auditing it, from a release candidate. Tooling that constructs `<repo>:<version>` by convention gets a 404 against a tag that was never created. So an inaccurate tag is a real defect even though the digest still resolves — it just fails later, and quietly, which is worse.

**Both must be correct. Neither is allowed to be approximate.**

## Three classes of tag

Which tag an image carries depends on what versions it. Confusing these classes is what makes a correct promotion look broken and a broken one look correct.

| Class | Tag shape | Example | Moves at release? |
| --- | --- | --- | --- |
| **Version-line** | the cozystack version | `cozystack-api:v1.5.0` | **Yes** — every release |
| **Component-versioned** | upstream version + cozystack suffix | `cluster-api-control-plane-provider-kamaji:v0.19.0-cozystack.0` | No |
| **Third-party pass-through** | whatever upstream publishes | `docker.io/library/busybox:1.37.0` | No |

Version-line images are first-party builds whose version *is* the cozystack version; they are the ones a release rewrites and retags. Component-versioned images are first-party rebuilds of an upstream component, versioned by that component so the provenance stays legible — kamaji keeping `v0.19.0-cozystack.0` across a cozystack release is correct, not a missed rewrite. Only a release build pushes that tag (`PUBLISH_VERSIONED=1`); every other build pushes the image under its build-unique `IMAGE_TAG`, so in a CI registry the component tag in the ref may not exist and the digest is what resolves. Third-party images are vendored by digest from registries cozystack cannot push to; they are never retagged and never mirrored.

Only class 1 is touched by a promotion. A tool that "fixes" a class 2 or 3 tag to the cozystack version is introducing a bug, not removing one.

## Three storage shapes

A reference can be stored in three different kinds of file, and **all three are first-class**. Every consumer must read all three. The single most repeated defect in this area is a tool that knows about one shape and silently ignores another; it does not fail, it just quietly skips images, and the gap surfaces a release later.

1. **`packages/<group>/<pkg>/values.yaml`** — the common case, in the YAML sub-shapes listed here: single string, split map with `digest`, split map with the digest inside `tag`, chart-global `global.registry.address` + `global.images[]`, the OCI-artifact `platformSourceUrl`/`platformSourceRef` pair, and upstream ingress-nginx's split map with the repository in `image` and a separate `digest`. The sub-shapes exist because vendored upstream charts choose their own values layout; the list is empirical, not closed.
2. **`packages/<group>/<pkg>/images/<name>.tag`** — a plain file holding one reference, read by templates via `.Files.Get` rather than through values. Easy to forget precisely because it never appears in a values file; otherwise an ordinary first-party reference that carries the cozystack version like any other.
3. **A declared file that embeds a reference in surrounding text** — used when a package vendors an upstream manifest and its `image:` target `sed`s the built reference straight into it. `packages/system/multus/templates/multus-daemonset-thick.yml` is the only instance today. It is vendored *and* patched: it carries Helm conditionals, so `yq` cannot parse it at all and the YAML pass yields nothing for it. What keeps its reference visible is the textual scrape `collect_image_refs` runs over every declared extra — for this entry that leg is not a backstop but the only one that works.

`hack/lib/image-refs.sh` is the single enumeration of all three, and the only place that knowledge is allowed to live. `image_ref_files <root>` yields the files; `collect_image_refs <root>` yields the references inside them. Shape 3 is an explicit list (`IMAGE_REF_EXTRA_FILES`) rather than a `templates/**` glob, because such a glob would sweep in vendored upstream chart templates and Helm-templated `image:` lines where a blind rewrite corrupts a value that `make update` regenerates. The declared entry is safe from that despite now containing Helm conditionals: its `image:` lines are plain, and declaring a file is the deliberate statement that its references are stamped rather than templated.

### Adding a reference in a new location

If a package stamps a reference somewhere the two globs do not cover, add that file to `IMAGE_REF_EXTRA_FILES` in `hack/lib/image-refs.sh` — and nowhere else. Every consumer picks it up at once. Do not teach an individual script about a new path; that is how the shapes diverged in the first place.

## Invariants

- **Every first-party reference is tag+digest.** A bare tag with no digest is not reproducible and must not be committed. `securitygroup-controller:v0.0.0` is a live violation.
- **Exactly one producer stamps each reference.** A reference no `Makefile` writes will never be refreshed by anything, including release preparation — `chBackupClientImage` in `packages/system/backupstrategy-controller/values.yaml` is a live violation, still pinned to `platform-migrations:v1.4.0-rc.2`.
- **A package that stamps a reference is in the root `Makefile` `build:` list.** `build-matrix.sh`, the main build and release preparation all iterate that one list, so a package outside it is never rebuilt by any path and its pin freezes permanently. `hack/build-matrix_test.bats` computes the set of packages whose Makefile runs `docker buildx build` and fails on any the list misses.
- **Every consumer of references reads all three storage shapes**, via `hack/lib/image-refs.sh`.
- **Scan wider than you rewrite.** A rewrite is deliberately narrow to avoid corrupting vendored defaults, so the check that follows it must not share that blind spot. `hack/promote-rewrite-tags.sh` rewrites the enumerated files, then greps the whole tree and fails if any reference to the release candidate survives.

## What each consumer does

| Tool | Reads | Job |
| --- | --- | --- |
| `hack/promote-rewrite-tags.sh` | all three shapes | rewrite `X.Y.Z-rc.N` → `X.Y.Z` in the tree, then fail if any survives |
| `hack/promote-packages-artifact.sh` | the rewritten packages tree | publish a reproducible temporary stable-candidate artifact and pin its digest in the installer |
| `hack/verify-promoted-packages.sh` | pinned candidate + merged tree + embedded rc artifact ref | before any stable tag is created, prove the candidate contains the merged tree and the rc's container digests |
| `hack/promote-retag.sh` | all three shapes | copy each digest to its stable tag in the registry |
| `hack/nightly-mirror.sh` | all three shapes | copy each digest to the public registry, then rewrite hosts in the same file set |
| `hack/overlay-main-images.sh` | `values.yaml` + `*.tag` | overlay current-main references onto packages a PR did not rebuild |
| `hack/stitch-multiarch.sh` | all three shapes | join each amd64 image and its `-arm64` twin into one index, repin the tree on it, then fail if any old digest survives |
| `hack/retention-packages.sh` | all three shapes, in the published nightly tree | name the GHCR packages the retention workflow prunes (those linked to another repository are then skipped); a ref it cannot see is a package whose nightlies are never pruned |
| `hack/verify-multiarch.sh` | all three shapes, first- and third-party | fail a release unless every pinned image is an index with `linux/amd64` and `linux/arm64`, bar the repositories in `hack/multiarch-allowlist` |

`hack/multiarch-allowlist` is the only bypass for that gate, and it is for images that cannot exist for arm64, not for images nobody has rebuilt yet. An entry is a repository with no tag or digest, so bumping an allowed image needs no edit, and it must carry a reason after `#` on the same line: the script refuses an entry without one. Allowed refs are still printed, under their own "allowed single-arch" heading, and in tree mode an entry that matches no ref that failed the check is reported as stale without changing the exit status, so it gets removed once the image is multi-arch or gone. `--refs-file` reports no stale entries, since a list of pulled images omits everything the run did not pull.

The gate reads this enumeration, so it covers exactly the refs pinned by digest in the three storage shapes. An image a vendored chart renders from its own defaults, or one an operator launches, is outside it. `verify-multiarch.sh --refs-file` checks a plain list of refs instead, tag-only ones resolved by tag, and the rc e2e feeds it `image-refs.txt` from `hack/collect-images.sh`: every image the sandbox nodes pulled, by the digest each pull resolved to. Neither covers a package that e2e never installs and the tree does not pin by digest.

Two rows do not source the shared library, for opposite reasons. `hack/overlay-main-images.sh` reads references and walks the tree itself, so it is a real exception to the invariant above: a file newly declared in `IMAGE_REF_EXTRA_FILES` does not automatically reach it. Its line classifier also has to know the bare-reference shape by itself: a `*.tag` file holds one reference with no YAML key around it, and until a build stamps it there is no `@sha256:` either, so a changed line there is recognised by shape rather than by key, scoped to `*.tag` because only there is a whole line known to be a reference and nothing else. `hack/promote-rewrite-tags.sh` carries a second match branch for that same shape and for the same reason. `hack/promote-packages-artifact.sh` is not a consumer of references at all — it hands the whole packages tree to `flux push artifact` and touches exactly one reference, the installer's own `platformSourceUrl`/`platformSourceRef` pair, by key with `yq`. Every shape travels inside the artifact because every file does, so there is nothing there for the enumeration to widen and no gap for a newly declared file to fall into.

`nightly-mirror.sh` is the one where a missed file is worst. The set it rewrites hosts in must equal the set it mirrored: a file rewritten but not mirrored leaves a dangling reference to an image never pushed, and a file mirrored but not rewritten leaves the published tree pointing at the private build registry.

## Known gaps

These predate the shared enumeration. Each entry says whether the tree works around it today; they are recorded here so nobody rediscovers them as surprises.

**The nightly host rewrite cannot reach a host split across two keys.** The primary pass is a literal `<src-registry>/` substring replace, so it only rewrites a reference whose host sits contiguously in front of the repository. Two layouts in the tree defeated it; one is handled by a second expression and the other was removed from the tree.

`kubeovn` puts the whole host in `global.registry.address` with the bare repository in a sibling key, so there is no trailing slash to match. This one **is** rewritten now, by a second, end-of-line-anchored expression that fires only when the source registry is the complete scalar value — a contiguous `<src>/<repo>@<digest>` ref continues past the host, so the two expressions can never both fire on one reference. It had to be fixed when kubeovn's image build moved into this repository: until then the image was built and pushed to the public registry by cozystack/kubeovn-chart, the baked ref kept the committed public-registry host, and the ownership filter skipped it. The moment a CI build stamps the build-registry host into that key, the image is mirrored but the host is not rewritten, and the published tree points at the private build registry. Covered by `hack/nightly-mirror_test.bats`.

`keycloak-operator` split at a different boundary: `registry: ghcr.io` beside `repository: cozystack/cozystack/<name>`, with an `image:` target that stamped only `tag`. A CI build would have rendered the committed public host with a digest that exists only in the build registry, and stamping the build host into `registry` instead would spread the source registry across two keys (`iad.ocir.io` and `idyksih5sir9/cozystack/<name>`), where no anchored replace can reach it. Rather than a structure-aware rewrite pass, the package now sets `registry: ""` and keeps the whole host in `repository`, which renders the same image string because the upstream chart joins the two keys, and its `image:` target stamps `repository` the same way. The rewrite still cannot reach a host split at that boundary, so a package must not stamp one: keep the host inside `repository`, or make it the complete value of its key as kubeovn does. Covered by `hack/nightly-mirror_test.bats`.

Collection understands both shapes — the host is rejoined, so a split-key ref carrying the source-registry host is still selected and mirrored. This is a defect in the host dimension; the version dimension the promote rewrite handles is unaffected, because the version always lives in a `tag` key regardless of where the host is.

**A reference inside a gzip is invisible.** `capi-providers-cpprovider` stamps its kamaji reference into `files/control-plane-components.yaml` and into the `files/components.gz` built from it, and the chart ships the `.gz`. Nothing enumerates either: declaring only the readable copy would rewrite what nothing consumes and diverge it from what does. Note the promote postcondition cannot catch this class at all — `grep -rIl` skips binary files — so it fails silently rather than loudly. No tag rewrite is owed (kamaji is component-versioned) and the image is still mirrored and retagged via its `.tag` file, so the residual is that a nightly's kamaji ConfigMap keeps the build-registry host. The fix is decompress, rewrite, recompress with `gzip -n` for reproducibility. `hack/stitch-multiarch.sh` does exactly that for its digest rewrite: it declares the `.gz` together with its plain source in its own `GZIP_COPIES`, rewrites both, and looks inside every `.gz` in the tree both before rewriting, to skip an image pinned in an undeclared one, and after, in its leftover-digest check. The rewritten `.gz` is byte-identical to what the package's `image` target writes only when both use GNU gzip, as CI does; Apple's gzip compresses to different bytes, so a stitch run on macOS rewrites the right content into a different file. The other consumers still leave both copies alone.

**`charts/` is excluded, and is almost always right.** Vendored upstream chart values are regenerated by `make update` and their images are neither built nor pushed here. The one exception is `packages/system/cozystack-scheduler/charts/cozystack-scheduler/values.yaml`, which pins a first-party-namespace image. Excluding it is still correct — it is built by a separate repository, component-versioned, and already published to the public registry, so no retag or mirror is owed — but "everything under `charts/` is third-party" is not literally true.

## Why promotion does not rebuild containers

Promotion copies the release candidate's container images to the stable tag **by digest**, so every executable image in the stable release is bit-for-bit what passed end-to-end testing. Nothing is recompiled. The packages OCI artifact is declarative configuration rather than a container image, and its content must change: `hack/promote-rewrite-tags.sh` replaces the rc tag strings in the tree, the workflow commits that exact content, then `hack/promote-packages-artifact.sh` serializes it under a run-specific temporary `promotion-*` tag and a second commit pins its new digest in the installer. Promotion first requires the target base to carry the candidate-aware pipeline. The promotion PR checks the candidate against its prospective merge; finalize repeats the check before creating any stable name. Both require the candidate and its embedded rc baseline to use the trusted public `cozystack-packages` repository, follow the old self-reference back to that rc artifact, and prove that all container digests stayed fixed.

The packages artifact includes `core/installer/values.yaml`, so it cannot contain its own digest: writing that digest changes the payload and produces another digest forever. The separately published installer chart carries the real candidate pin used at runtime; the older `platformSourceRef` embedded inside the packages artifact is inert because the platform PackageSource does not install `core/installer`. Verification normalizes exactly that field and rejects every other difference. It also models Flux's established archive semantics: symlinks in the source tree are omitted rather than followed, matching the RC packages artifacts produced by the existing build.

## Testing

`hack/promote-rewrite-tags_test.bats` round-trips the rewrite against the real tree: it stamps a synthetic release-candidate version into every first-party reference, runs the rewrite, and asserts nothing retains it. `hack/promote-packages-artifact_test.bats` covers deterministic candidate naming, metadata and digest pinning; `hack/verify-promoted-packages_test.bats` proves the self-reference exception is narrow and catches rc refs or post-publish tree drift. These checks run under `make unit-tests` without a cluster, a registry or a release. Any change to how references are stored or promoted must keep them passing.
