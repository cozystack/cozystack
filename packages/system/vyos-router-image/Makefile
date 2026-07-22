NAME=vyos-router-image
NAMESPACE=cozy-system

# ── Pinned VyOS build inputs (reproducibility levers) ────────────────────────
# The build TOOLING is pinned by digest/commit; the upstream rolling apt mirror
# (packages.vyos.net/repositories/rolling) floats, so this is pinned-inputs /
# best-effort reproducible, not bit-identical. Bump these deliberately: the image
# and the site-router cloud-init contract advance atomically (see
# packages/apps/site-router/docs/image-lifecycle.md).
VYOS_BUILD_IMAGE = vyos/vyos-build:current@sha256:77b88166bb9dfb2fdef4f870064e579a1e33247a52c03a12c1703bf9b854a4c5
VYOS_BUILD_REF   = 51ee73ec541b6e28ee430607ab93665ca8c13a93
VYOS_VERSION     = 1.5-rolling-20260720
VYOS_ARCH        = amd64

include ../../../hack/common-envs.mk

QCOW2 = ../../../_out/assets/vyos-router-$(VYOS_ARCH).qcow2

# Package the qcow2 as a KubeVirt containerDisk OCI image, push it, and stamp the
# digest .tag into the CONSUMING chart (vm-default-images), mirroring how the
# talos build stamps bootbox/images/matchbox.tag. In CI the build-vyos job's
# `git diff` captures that stamped .tag as a patch fragment, which `finalize`
# reassembles into pr.patch so e2e resolves the freshly-built digest.
image: image-vyos-router-disk

image-vyos-router-disk: qcow2
	docker buildx build -f images/vyos-router-disk/Dockerfile ../../.. \
		$(call image-tags,vyos-router-disk,$(VYOS_VERSION)) \
		--metadata-file images/vyos-router-disk.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/vyos-router-disk:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/vyos-router-disk.json -o json -r)" \
		> ../vm-default-images/images/vyos-router-disk.tag
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

update: ;
