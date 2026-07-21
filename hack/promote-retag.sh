#!/bin/sh
# Promote a release-candidate to stable by RETAGGING its already-built images —
# no rebuild. Every cluster artifact cozystack ships (component images, the
# cozystack-packages OCI artifact) is content-addressed; this script reads the
# digests the rc baked into the package values.yaml files and copies each, by
# digest, to the stable tag. Because the copy source is the immutable digest,
# the stable image is bit-for-bit the rc image that passed e2e — promotion
# cannot diverge from what was tested.
#
# Usage: hack/promote-retag.sh <stable-version> [--dry-run]
#   <stable-version>  e.g. v1.4.0  (the tag the rc is promoted to)
#   --dry-run         print the skopeo copies without executing them
#
# Environment:
#   MOVE_LATEST=1     also (re)point :latest at each promoted image. OFF by
#                     default: :latest must move only when the version being
#                     promoted is the newest published stable, otherwise a patch
#                     on an older line (e.g. v1.4.6 while 1.5.x is current) would
#                     drag :latest backwards. The caller (finalize) computes this
#                     with the same max-semver test it uses for the release's
#                     make_latest flag and sets MOVE_LATEST accordingly.
#
# Reads image refs from packages/*/*/values.yaml in the CURRENT tree, which is
# expected to be the promoted stable digest-vendored tree (the release-X.Y.Z
# branch, whose digests are the rc's — only the cosmetic tag string differs).
# Requires: yq (mikefarah), skopeo, and a registry login already done.
#
# The repo/tag split (ref_repo below) strips the :tag from the last path
# component only, so registry hosts that carry a :port are preserved.
set -eu

STABLE="${1:?usage: promote-retag.sh <stable-version> [--dry-run]}"
DRY_RUN=0
[ "${2:-}" = "--dry-run" ] && DRY_RUN=1
# :latest is repointed only when the caller asserts this is the newest stable.
MOVE_LATEST="${MOVE_LATEST:-0}"

# Only cozystack-owned images (those the build pushed to $REGISTRY) are
# retagged. Everything else vendored by digest — third-party images and bare
# upstream tags — lives in registries this job cannot push to. Override
# REGISTRY to match a fork's build registry; the default mirrors
# hack/common-envs.mk.
REGISTRY="${REGISTRY:-ghcr.io/cozystack/cozystack}"

command -v yq >/dev/null     || { echo "yq (mikefarah) is required" >&2; exit 1; }
# The queries below use mikefarah syntax; reject python-yq and other variants
# (mirrors the build-deps check in the Makefile).
yq --version 2>&1 | grep -q mikefarah || { echo "yq (mikefarah) is required" >&2; exit 1; }
# skopeo is only needed to actually copy; a --dry-run just prints the plan.
[ "$DRY_RUN" -eq 1 ] || command -v skopeo >/dev/null || { echo "skopeo is required" >&2; exit 1; }

# Collect "repo@sha256:..." refs from every package values.yaml. The shapes
# below are the ones present in the tree today, found by auditing every
# @sha256: digest under packages/*/*/values.yaml against this selector's
# output — NOT a closed set the build is known to be limited to. Charts are
# vendored from upstream and their values layout is theirs to change, so treat
# this list as empirical and re-run that audit when a package is added:
#   1. single string  <repo>:<tag>@sha256:<digest>   (e.g. .cozystackAPI.image)
#   2. split map       {[registry,] repository, tag, digest}      (e.g. .cilium.image)
#   3. split map       {[registry,] repository, tag: <tag>@sha256:<digest>}
#                      (e.g. .linstorCSI.image; .keycloak-operator.image adds registry)
#   4. chart-global    global.registry.address + global.images.<n>.{repository, tag}
#                      (kube-ovn's wrapper chart)
#   5. OCI artifact    {platformSourceUrl: oci://<repo>, platformSourceRef: digest=sha256:<digest>}
#
# The optional `registry` sibling in shapes 2/3 and the whole of shape 4 exist
# because the host does not always live inside `repository`. When it does not,
# the rule must rejoin it: a host-less ref reaches the ownership filter below
# looking third-party and is dropped, which is the same silent-skip failure
# this selector's shape-3 rule was added to fix. keycloak-operator
# (registry: ghcr.io + repository: cozystack/cozystack/keycloak-operator) and
# kubeovn (global.registry.address + repository: kubeovn) are both built and
# pushed to $REGISTRY by cozystack — kubeovn by the cozystack/kubeovn-chart
# wrapper repo, whose own `make image` writes exactly the shape-4 layout — and
# both went untagged for every 1.x release until these rules landed.
#
# Shape 3 is the dominant one: most package Makefiles set `.image.tag` to
# "$(IMAGE_TAG)@$(digest)" in a single yq call instead of maintaining a separate
# `digest` key. It matches neither shape 2 (no `digest` key) nor, usefully, shape
# 1 — that rule sees only the bare tag value, which carries no repository, so
# ref_repo() reduces "<tag>@sha256:<digest>" to "<tag>" and the ownership filter
# drops it. Omitting this rule silently skipped eight images across six packages
# (kamaji, kilo, linstor-csi, piraeus-server, linstor-gui, metallb-controller,
# metallb-speaker, redis-operator): the promotion reported success while never
# creating their :<version> tags. Unlike the nightly mirror this retags within one
# registry, so the digests still resolve and digest-pinned installs are unaffected
# — but the release does not carry the tags it claims to, and nothing detects it.
#
# The `tag == "!!str"` guard is load-bearing: yq's test() aborts the expression on
# a non-string tag ("cannot match with !!int"), and since this invocation swallows
# stderr and status, that abort would silently drop every shape-3 ref in the same
# file. `tag: 1.24` unquoted in a neighbouring third-party block is enough. Rule 1
# is immune — its `select(tag == "!!str")` already precedes its test().
collect_refs() {
  for f in packages/*/*/values.yaml; do
    [ -f "$f" ] || continue
    # shape 1
    yq -r '.. | select(tag == "!!str") | select(test("@sha256:[0-9a-f]{64}"))' "$f" 2>/dev/null || true
    # shape 2. The `sub("^/"; "")` is what makes `registry` optional: absent, it
    # alternates to "" and leaves a leading slash on the join, which is stripped
    # back to the bare repository the rule emitted before registry was handled.
    yq -r '.. | select(tag == "!!map") | select(has("repository") and has("digest")) | (((.registry // "") + "/" + .repository) | sub("^/"; "")) + "@" + .digest' "$f" 2>/dev/null || true
    # shape 3
    yq -r '.. | select(tag == "!!map") | select(has("repository") and has("tag")) | select(.tag | tag == "!!str") | select(.tag | test("@sha256:[0-9a-f]{64}")) | (((.registry // "") + "/" + .repository) | sub("^/"; "")) + "@" + (.tag | sub(".*@"; ""))' "$f" 2>/dev/null || true
    # shape 4. Scoped to global.images rather than a recursive descent: the host
    # is a document-level key, so binding it to a map found anywhere in the file
    # would attach kube-ovn's registry to unrelated repositories. Guarding on a
    # non-empty address keeps a chart without one from emitting "/<repo>".
    # $reg is a yq binding, not a shell variable — the single quotes are
    # required, so SC2016's "expressions don't expand" is inverted here.
    # shellcheck disable=SC2016
    yq -r '(.global.registry.address // "") as $reg | select($reg != "") | .global.images[] | select(tag == "!!map") | select(has("repository") and has("tag")) | select(.tag | tag == "!!str") | select(.tag | test("@sha256:[0-9a-f]{64}")) | $reg + "/" + .repository + "@" + (.tag | sub(".*@"; ""))' "$f" 2>/dev/null || true
    # shape 5
    yq -r '.. | select(tag == "!!map") | select(has("platformSourceUrl") and has("platformSourceRef")) | (.platformSourceUrl | sub("^oci://"; "")) + "@" + (.platformSourceRef | sub("^digest="; ""))' "$f" 2>/dev/null || true
  done
}

# Split a "<repo>[:<tag>]@sha256:<digest>" ref into repo and digest.
# ref_repo strips the :tag from the LAST path component only, so a registry
# host that carries a :port (e.g. localhost:5000/cozystack/operator, with no
# tag) keeps its port instead of being truncated to the host.
ref_repo() {
  r="${1%@*}"            # strip @digest
  img="${r##*/}"         # last path component (may carry :tag)
  if [ "$r" = "$img" ]; then
    printf '%s' "${img%:*}"
  else
    printf '%s/%s' "${r%/*}" "${img%:*}"
  fi
}
ref_digest() { printf '%s' "${1##*@}"; }               # sha256:...

copy() {
  _src="$1"; _dst="$2"
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "DRY-RUN skopeo copy --multi-arch all docker://$_src docker://$_dst"
    return 0
  fi
  # --multi-arch all copies the whole manifest list (every platform); it is
  # mutually exclusive with the deprecated --all alias, do not combine them.
  skopeo copy --multi-arch all "docker://$_src" "docker://$_dst"
}

# Normalize every collected ref to the canonical "<repo>@<digest>" form (drops
# the cosmetic :tag from shape 1) so the dedup is on the real identity — the
# same image vendored in two shapes is retagged once, not twice.
refs=""
for raw in $(collect_refs); do
  [ -n "$raw" ] || continue
  _repo="$(ref_repo "$raw")"
  # Retag only cozystack-owned images: those the build pushes to $REGISTRY, so
  # a skopeo copy can succeed. Everything else is vendored by digest from a
  # registry this job cannot push to, and a copy there would fail and (under
  # set -e) abort the whole promotion. What this drops today:
  #   - third-party hosts: docker.io/clastix/kubectl, ghcr.io/kvaps/...,
  #     ghcr.io/lexfrei/{kuberture,ouroboros} (deliberately not mirrored under
  #     ghcr.io/cozystack — see those packages' values.yaml)
  #   - ghcr.io/cozystack/ingress-nginx-with-protobuf-exporter/*, which is a
  #     cozystack-org repo but sits outside $REGISTRY's path
  #   - non-ref scalars, e.g. a "--migrate-image=..." arg string
  # Note kilo, kube-ovn and keycloak-operator are NOT in that list: all three
  # are built and pushed to $REGISTRY, and are selected by shapes 3 and 4. An
  # earlier version of this comment named them as unpushed third parties, which
  # is what let their missing release tags go unnoticed.
  case "$_repo" in
    "${REGISTRY}/"*) ;;
    *) continue ;;
  esac
  refs="${refs}${_repo}@$(ref_digest "$raw")
"
done
refs="$(printf '%s' "$refs" | sort -u)"
[ -n "$refs" ] || { echo "No cozystack-owned digest-pinned image refs found under ${REGISTRY}/ — is this the rc's baked tree?" >&2; exit 1; }

echo "$refs" | while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  repo="${ref%@*}"
  digest="${ref##*@}"
  echo "▸ ${repo}  ${digest}"
  # The stable tag is write-once at the image level: inspect the destination
  # before copying. No-op if it already points at this rc digest (idempotent
  # re-run), fail if it points elsewhere (a partial run or manual push already
  # put different bytes there) rather than mutate released bytes. :latest is
  # intentionally mutable and (re)pointed below only when MOVE_LATEST=1.
  if [ "$DRY_RUN" -eq 0 ]; then
    cur="$(skopeo inspect --format '{{.Digest}}' "docker://${repo}:${STABLE}" 2>/dev/null || echo '')"
    if [ -n "$cur" ] && [ "$cur" != "$digest" ]; then
      echo "::error::${repo}:${STABLE} already exists at '${cur}'; refusing to move it to '${digest}' (stable image tags are write-once)" >&2
      exit 1
    fi
    if [ "$cur" = "$digest" ]; then
      echo "  = ${repo}:${STABLE} already at ${digest}; skipping stable copy"
    else
      copy "$ref" "${repo}:${STABLE}"
    fi
  else
    copy "$ref" "${repo}:${STABLE}"
  fi
  [ "$MOVE_LATEST" = "1" ] && copy "$ref" "${repo}:latest"
  # Verify the stable tag now resolves to the exact rc digest (skip in dry-run).
  if [ "$DRY_RUN" -eq 0 ]; then
    got="$(skopeo inspect --format '{{.Digest}}' "docker://${repo}:${STABLE}" 2>/dev/null || echo '')"
    if [ "$got" != "$digest" ]; then
      echo "::error::${repo}:${STABLE} resolved to '${got}', expected '${digest}'" >&2
      exit 1
    fi
  fi
done

if [ "$MOVE_LATEST" = "1" ]; then
  echo "Retagged image refs to ${STABLE} (+latest)."
else
  echo "Retagged image refs to ${STABLE} (:latest left unmoved; MOVE_LATEST=0)."
fi
