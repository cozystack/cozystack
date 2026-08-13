NAME=vyos-router-image
NAMESPACE=cozy-system

# ── Pinned VyOS build inputs (reproducibility levers) ────────────────────────
# The build TOOLING is pinned by digest/commit; the upstream rolling apt mirror
# (packages.vyos.net/repositories/rolling) floats, so this is pinned-inputs /
# best-effort reproducible, not bit-identical. Bump these deliberately: the image
# and the site-router cloud-init contract advance atomically (see
# packages/apps/site-router/docs/image-lifecycle.md).
#
# VYOS_BUILD_REF CANNOT be left to age. It selects the kernel the live-build asks
# for (data/defaults.toml `kernel_version`), and the rolling mirror carries only
# the current one — so a ref that has fallen behind fails with
# `E: Unable to locate package linux-image-<old>-vyos` rather than building
# something stale. That is not a flake to retry: the pin has to move.
#
# What actually watches these, stated precisely, because the previous wording
# here claimed a guard that does not stop this break and the build broke anyway:
#
#   - .github/renovate.json carries two custom regex managers, one for
#     VYOS_BUILD_IMAGE (docker datasource) and one for VYOS_BUILD_REF (git-refs
#     against the upstream `rolling` branch). Nothing else can see a pin held in
#     a Makefile variable, so without them there is no watcher at all.
#   - They only see this file once it is on the DEFAULT branch. Renovate does not
#     evaluate an unmerged branch's config, so for as long as this package lives
#     on a feature branch both pins are unwatched by construction. That is how
#     VYOS_BUILD_REF fell behind far enough to break the build before it ever
#     landed.
#   - Even once merged they raise a PR; they do not hold the build up. The break
#     is on the MIRROR's kernel-retention clock, not on Renovate's PR clock, so a
#     bump PR left unmerged past the point the old kernel is dropped fails
#     exactly the same way. Treat an open vyos-build ref PR as time-bounded work,
#     not as a routine dependency bump.
#
# VYOS_VERSION is a label with no upstream release feed and stays a manual bump.
# Keep it as `1.5-rolling-<ref commit date, YYYYMMDD>` so it is a function of
# VYOS_BUILD_REF rather than of the day someone rebuilt: bump the ref, set this
# to that commit's date. It names the snapshot in the artifact and the image tag
# and is otherwise inert — build-qcow2.sh globs for the qcow2 rather than
# reconstructing the name from it.
#
# Verify a bump before trusting it by comparing the ref's kernel_version against
# what the mirror actually serves — the two must be equal. Quote the gh URL: the
# `?` is a glob character and zsh will refuse the command unquoted.
#   gh api "repos/vyos/vyos-build/contents/data/defaults.toml?ref=<ref>" \
#     --jq .content | base64 -d | grep kernel_version
#   curl -fsSL https://packages.vyos.net/repositories/rolling/dists/rolling/main/binary-amd64/Packages.gz \
#     | gunzip | grep -oE '^Package: linux-image-[0-9.]+-vyos$' | sort -u
VYOS_BUILD_IMAGE = vyos/vyos-build:current@sha256:77b88166bb9dfb2fdef4f870064e579a1e33247a52c03a12c1703bf9b854a4c5
VYOS_BUILD_REF   = e14a4895cd5add37240fca9da195832b8a8683e3
VYOS_VERSION     = 1.5-rolling-20260811
VYOS_ARCH        = amd64

include ../../../hack/common-envs.mk

QCOW2 = ../../../_out/assets/vyos-router-$(VYOS_ARCH).qcow2

# Package the qcow2 as a KubeVirt containerDisk OCI image, push it, and stamp the
# digest .tag into the CONSUMING chart (packages/apps/site-router), whose boot
# DataVolume imports it via CDI's registry importer — mirroring how the talos
# build stamps bootbox/images/matchbox.tag. In CI the build-vyos job's `git diff`
# captures that stamped .tag as a patch fragment, which `finalize` reassembles
# into pr.patch so e2e resolves the freshly-built digest.
image: image-vyos-router-disk

image-vyos-router-disk: qcow2
	docker buildx build -f images/vyos-router-disk/Dockerfile ../../.. \
		$(call image-tags,vyos-router-disk,$(VYOS_VERSION)) \
		--metadata-file images/vyos-router-disk.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/vyos-router-disk:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/vyos-router-disk.json -o json -r)" \
		> ../../apps/site-router/images/vyos-router-disk.tag
	rm -f images/vyos-router-disk.json

# Heavy privileged live-build step; produces $(QCOW2). Kept idempotent so a rerun
# reuses an already-built disk.
qcow2:
	test -f $(QCOW2) || \
		VYOS_BUILD_IMAGE="$(VYOS_BUILD_IMAGE)" \
		VYOS_BUILD_REF="$(VYOS_BUILD_REF)" \
		VYOS_VERSION="$(VYOS_VERSION)" \
		VYOS_ARCH="$(VYOS_ARCH)" \
		hack/build-qcow2.sh

# No vendored upstream chart to pull, and the build-input pins above are
# refreshed by Renovate rather than by `make update`.
update: ;
