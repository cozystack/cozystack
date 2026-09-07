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
VYOS_BUILD_REF   = d2525d97a11de9f4799dbb6d425c5c9c51edf179
VYOS_VERSION     = 1.5-rolling-20260904
VYOS_ARCH        = amd64

include ../../../hack/common-envs.mk

QCOW2 = ../../../_out/assets/vyos-router-$(VYOS_ARCH).qcow2

# Package the qcow2 as a KubeVirt containerDisk OCI image, push it, and stamp the
# digest .tag into the CONSUMING chart (packages/apps/site-router), whose boot
# DataVolume imports it via CDI's registry importer — mirroring how the talos
# build stamps bootbox/images/matchbox.tag. In CI the build-vyos job's `git diff`
# captures that stamped .tag as a patch fragment, which `finalize` reassembles
# into pr.patch so e2e resolves the freshly-built digest.
# The appliance is content-keyed by VYOS_VERSION, and the pins above make that a
# function of VYOS_BUILD_REF. So the live-build below runs exactly ONCE per pin:
# every later build resolves the already-published manifest and stamps its digest
# instead. That is what keeps the floating rolling apt mirror off the critical
# path of `make build` — which matters because this package must STAY in the root
# `build:` list (docs/agents/image-refs.md: a package that stamps a reference and
# is not in that list never gets rebuilt by any path and its pin freezes), and
# `build:` runs on every push to main.
#
# The cache tag is suppressed under OCI_EXPORT_DIR for the reason image-tags
# documents: a multi-`--tag` `--output type=oci` build writes every ref into the
# archive's index.json and `skopeo copy oci-archive:` then refuses it (#3257).
# published-appliance-digest.sh declines to answer in that case too, so an export
# build always builds and always produces its archive.
APPLIANCE_TAG = appliance-$(VYOS_VERSION)
APPLIANCE_REF = $(REGISTRY)/vyos-router-disk:$(APPLIANCE_TAG)
APPLIANCE_CACHE_TAG = $(if $(strip $(OCI_EXPORT_DIR)),,--tag $(APPLIANCE_REF))
STAMP = ../../apps/site-router/images/vyos-router-disk.tag

image: image-vyos-router-disk

# Each branch stamps a tag that provably exists in its own context: the hit path
# stamps the appliance tag it just resolved, the build path stamps the
# build-unique tag image-tags always emits. The digest decides what runs either
# way, and nightly-mirror.sh keys on repo+digest rather than on the tag text.
image-vyos-router-disk:
	@digest="$$(hack/published-appliance-digest.sh "$(APPLIANCE_REF)" || true)"; \
	if [ -n "$$digest" ]; then \
		echo "I: appliance $(VYOS_VERSION) already published, reusing $$digest"; \
		printf '%s@%s\n' "$(APPLIANCE_REF)" "$$digest" > $(STAMP); \
	else \
		echo "I: appliance $(VYOS_VERSION) not published yet, building it"; \
		$(MAKE) appliance-publish; \
	fi

appliance-publish: qcow2
	docker buildx build -f images/vyos-router-disk/Dockerfile ../../.. \
		$(APPLIANCE_CACHE_TAG) \
		$(call image-tags,vyos-router-disk,$(VYOS_VERSION)) \
		--metadata-file images/vyos-router-disk.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/vyos-router-disk:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/vyos-router-disk.json -o json -r)" \
		> $(STAMP)
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
