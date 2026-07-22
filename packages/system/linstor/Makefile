export NAME=linstor
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

LINSTOR_VERSION ?= 1.33.3
LINSTOR_CSI_VERSION ?= v1.11.2

test:
	helm unittest .

# LINSTOR is replaced by Blockstor. The blockstor
# controller/apiserver/satellite images are the published
# ghcr.io/cozystack/blockstor-* images (pinned by digest in values.yaml),
# NOT built in-tree — so image-piraeus-server is a no-op stub kept only
# so the root `make build` target (which calls `image`) does not fail.
# linstor-csi is still built here because it is reused unchanged.
image: image-piraeus-server image-linstor-csi

image-piraeus-server:
	@echo "[linstor] image-piraeus-server: no-op (uses published ghcr.io/cozystack/blockstor-* images)"

image-linstor-csi:
	docker buildx build images/linstor-csi \
		--build-arg VERSION=$(LINSTOR_CSI_VERSION) \
		$(call image-tags,linstor-csi,$(LINSTOR_CSI_VERSION)) \
		$(call cache-args,linstor-csi) \
		--metadata-file images/linstor-csi.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/linstor-csi" \
		yq -i '.linstorCSI.image.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/linstor-csi.json -o json -r)" \
		yq -i '.linstorCSI.image.tag = strenv(TAG)' values.yaml
	rm -f images/linstor-csi.json
