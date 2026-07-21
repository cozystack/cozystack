# shellcheck shell=sh
# Shared enumeration of where cozystack vendors its image references.
#
# Sourced by hack/promote-retag.sh, hack/nightly-mirror.sh and
# hack/promote-rewrite-tags.sh. It exists because those three call sites each
# grew their own idea of where a ref can live, and drifted: promote-retag and
# nightly-mirror scanned only the depth-2 values.yaml, while the promote
# workflow's tag rewrite scanned those plus packages/apps/kubernetes/images/
# *.tag alone. Every ref stored in any OTHER images/*.tag file was therefore
# invisible to all three — never retagged to the stable version in the
# registry, never mirrored to GHCR for a nightly, and left carrying the rc
# version string in a promoted release tree. See docs/agents/image-refs.md for
# the contract this file implements; a new storage location must be added here
# and nowhere else.
#
# Three storage shapes exist, and all are first-class:
#
#   1. <root>/<group>/<pkg>/values.yaml     — five YAML sub-shapes, below
#   2. <root>/<group>/<pkg>/images/*.tag    — a plain file holding one ref
#   3. an explicitly declared file (IMAGE_REF_EXTRA_FILES) whose producer sed's
#      a ref straight into it because the package is not values-driven
#
# The .tag files are consumed by templates via .Files.Get rather than through
# values, which is why they are easy to forget; they are otherwise ordinary
# first-party refs and carry the cozystack version exactly like values.yaml.
#
# <root> is "packages" for the committed tree and the extracted artifact
# directory for a baked tree; both use the same <group>/<pkg> layout below it.

# Root-relative files that hold a first-party ref outside the two globs above.
# Kept as an explicit list rather than a templates/** glob: that glob would
# sweep in every vendored upstream chart template and every Helm-templated
# `image:` line, where a blind rewrite corrupts a value `make update`
# regenerates. An entry here is a deliberate statement that some package's
# `image:` target sed's a ref into a file it vendors verbatim.
#
# system/multus is the only one today: its templates/ is the upstream
# multus-daemonset-thick.yml fetched by `make update`, and multus's Makefile
# `image:` target sed's the built ref into two `image:` lines inside it. Left
# undeclared, multus-cni is invisible to promotion and to the nightly mirror —
# it receives no stable :vX.Y.Z tag and is never copied to the public registry.
#
# KNOWN GAP, deliberately not listed: system/capi-providers-cpprovider stamps
# its kamaji ref into files/control-plane-components.yaml AND into the
# files/components.gz built from it, and the chart ships the .gz
# (templates/configmaps.yaml reads it with .Files.Get). Declaring only the
# readable .yaml would rewrite the copy nothing consumes and leave it diverged
# from the copy that does, which is worse than leaving both alone. Nothing is
# currently owed to it: kamaji is component-versioned (v0.19.0-cozystack.N), so
# no tag rewrite is due, and the image is still retagged and mirrored via
# images/cluster-api-control-plane-provider-kamaji.tag, which carries the same
# repo@digest. The residual is that a nightly's kamaji ConfigMap keeps the
# private build-registry host, since the mirror's host rewrite cannot reach
# inside a gzip. Fixing it means decompress/rewrite/recompress with `gzip -n`
# to stay reproducible — see docs/agents/image-refs.md.
# Assigned unconditionally rather than via ${VAR:-default}: this list is
# safety-critical for the retag and the mirror, neither of which has a
# postcondition that would notice it being wrong. An inherited environment
# variable able to silently NARROW it is a hazard with no matching use case.
# Word splitting is intended (the list is whitespace-separated), which also
# means a path containing whitespace cannot be represented here.
IMAGE_REF_EXTRA_FILES="system/multus/templates/multus-daemonset-thick.yml"

# Emit every ref-bearing file under <root>, one per line.
#
# Both globs are depth-anchored rather than a `find`: a deeper values.yaml
# belongs to a vendored upstream chart (packages/*/*/charts/**) whose images
# the build neither pushes nor stamps, and rewriting one would corrupt a
# vendored default that `make update` regenerates. No .tag file lives under a
# charts/ subtree today, so the images/*.tag glob is exact rather than merely
# conservative.
image_ref_files() {
  _ir_root="${1:?image_ref_files: <root> required}"
  for _ir_f in "$_ir_root"/*/*/values.yaml; do
    [ -f "$_ir_f" ] && printf '%s\n' "$_ir_f"
  done
  for _ir_f in "$_ir_root"/*/*/images/*.tag; do
    [ -f "$_ir_f" ] && printf '%s\n' "$_ir_f"
  done
  for _ir_f in $IMAGE_REF_EXTRA_FILES; do
    [ -f "$_ir_root/$_ir_f" ] && printf '%s\n' "$_ir_root/$_ir_f"
  done
  return 0
}

# Emit every "<repo>[:<tag>]@sha256:<digest>" ref found under <root>.
#
# The YAML shapes below are the ones present in the tree today, found by
# auditing every @sha256: digest under the depth-2 values.yaml against this
# selector's output — NOT a closed set the build is known to be limited to.
# Charts are vendored from upstream and their values layout is theirs to
# change, so treat the list as empirical and re-run that audit when a package
# is added:
#   1. single string  <repo>:<tag>@sha256:<digest>   (e.g. .cozystackAPI.image)
#   2. split map      {[registry,] repository, tag, digest}   (e.g. .cilium.image)
#   3. split map      {[registry,] repository, tag: <tag>@sha256:<digest>}
#                     (e.g. .linstorCSI.image; .keycloak-operator.image adds registry)
#   4. chart-global   global.registry.address + global.images.<n>.{repository, tag}
#                     (kube-ovn's wrapper chart)
#   5. OCI artifact   {platformSourceUrl: oci://<repo>, platformSourceRef: digest=sha256:<digest>}
#
# The optional `registry` sibling in shapes 2/3 and the whole of shape 4 exist
# because the host does not always live inside `repository`. When it does not,
# the rule must rejoin it: a host-less ref reaches the caller's ownership
# filter looking third-party and is dropped, which is the silent-skip failure
# the shape-3 rule was added to fix.
#
# Shape 3 is the dominant one: most package Makefiles set `.image.tag` to
# "$(IMAGE_TAG)@$(digest)" in a single yq call instead of maintaining a
# separate `digest` key. It matches neither shape 2 (no `digest` key) nor,
# usefully, shape 1 — that rule sees only the bare tag value, which carries no
# repository, so a caller's ref_repo() reduces "<tag>@sha256:<digest>" to
# "<tag>" and the ownership filter drops it.
#
# The `tag == "!!str"` guard is load-bearing: yq's test() aborts the expression
# on a non-string tag ("cannot match with !!int"), and since these invocations
# swallow stderr and status, that abort would silently drop every shape-3 ref
# in the same file. `tag: 1.24` unquoted in a neighbouring third-party block is
# enough. Rule 1 is immune — its select(tag == "!!str") precedes its test().
#
# `registry`, `repository` and `digest` are guarded the same way against a
# different failure: they are string-concatenated rather than test()ed, and yq
# coerces most scalars on `+`, so only a !!map aborts ("!!str () cannot be
# added to a !!map") and takes the whole file's expression with it. Being a
# select() rather than a test(), the guard drops just the offending map.
collect_image_refs() {
  _ir_root="${1:?collect_image_refs: <root> required}"

  for _ir_f in "$_ir_root"/*/*/values.yaml; do
    [ -f "$_ir_f" ] || continue
    # shape 1
    yq -r '.. | select(tag == "!!str") | select(test("@sha256:[0-9a-f]{64}"))' "$_ir_f" 2>/dev/null || true
    # shape 2. The sub("^/"; "") is what makes `registry` optional: absent, it
    # alternates to "" and leaves a leading slash on the join, stripped back to
    # the bare repository the rule emitted before registry was handled.
    yq -r '.. | select(tag == "!!map") | select(has("repository") and has("digest")) | select(.repository | tag == "!!str") | select(.digest | tag == "!!str") | select((.registry // "") | tag == "!!str") | (((.registry // "") + "/" + .repository) | sub("^/"; "")) + "@" + .digest' "$_ir_f" 2>/dev/null || true
    # shape 3
    yq -r '.. | select(tag == "!!map") | select(has("repository") and has("tag")) | select(.tag | tag == "!!str") | select(.tag | test("@sha256:[0-9a-f]{64}")) | select(.repository | tag == "!!str") | select((.registry // "") | tag == "!!str") | (((.registry // "") + "/" + .repository) | sub("^/"; "")) + "@" + (.tag | sub(".*@"; ""))' "$_ir_f" 2>/dev/null || true
    # shape 4. Scoped to global.images rather than a recursive descent: the
    # host is a document-level key, so binding it to a map found anywhere in
    # the file would attach kube-ovn's registry to unrelated repositories.
    # $reg is a yq binding, not a shell variable — the single quotes are
    # required, so SC2016's "expressions don't expand" is inverted here.
    # shellcheck disable=SC2016
    yq -r '(.global.registry.address // "") as $reg | select($reg != "") | select($reg | tag == "!!str") | .global.images[] | select(tag == "!!map") | select(has("repository") and has("tag")) | select(.tag | tag == "!!str") | select(.tag | test("@sha256:[0-9a-f]{64}")) | select(.repository | tag == "!!str") | $reg + "/" + .repository + "@" + (.tag | sub(".*@"; ""))' "$_ir_f" 2>/dev/null || true
    # shape 5
    yq -r '.. | select(tag == "!!map") | select(has("platformSourceUrl") and has("platformSourceRef")) | (.platformSourceUrl | sub("^oci://"; "")) + "@" + (.platformSourceRef | sub("^digest="; ""))' "$_ir_f" 2>/dev/null || true
  done

  # Storage shape 2: a .tag file holds one ref as plain text. Select on the
  # digest rather than emitting the whole file so a blank line or a comment a
  # future file may carry cannot reach the caller as a bogus ref. grep's
  # no-match exit 1 is swallowed for the same reason the yq calls swallow
  # theirs: an unmatched file is normal, not an error.
  for _ir_f in "$_ir_root"/*/*/images/*.tag; do
    [ -f "$_ir_f" ] || continue
    grep -Eo '[^[:space:]]+@sha256:[0-9a-f]{64}' "$_ir_f" 2>/dev/null || true
  done

  # Storage shape 3: declared files that embed a ref in surrounding text. The
  # same digest-anchored extraction applies — in a k8s manifest it lifts the
  # ref out of an `image: <ref>` line. Any third-party ref it also picks up is
  # discarded by the caller's ownership filter, exactly as for the other shapes.
  for _ir_f in $IMAGE_REF_EXTRA_FILES; do
    [ -f "$_ir_root/$_ir_f" ] || continue
    grep -Eo '[^[:space:]]+@sha256:[0-9a-f]{64}' "$_ir_root/$_ir_f" 2>/dev/null || true
  done

  return 0
}
