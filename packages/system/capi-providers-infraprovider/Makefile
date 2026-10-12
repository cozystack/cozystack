export NAME=capi-providers-infraprovider
export NAMESPACE=cozy-cluster-api

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: image

UPSTREAM_VERSION = $(shell awk -F= '/^ARG VERSION=/{print $$2}' images/cluster-api-provider-kubevirt/Dockerfile)
COZYSTACK_REV = 0

image:
	docker buildx build images/cluster-api-provider-kubevirt \
		$(call image-tags,cluster-api-provider-kubevirt,$(UPSTREAM_VERSION)-cozystack.$(COZYSTACK_REV)) \
		$(call cache-args,cluster-api-provider-kubevirt) \
		--metadata-file images/cluster-api-provider-kubevirt.json \
		$(BUILDX_ARGS)
	digest=$$(yq -e '."containerimage.digest" | select(test("^sha256:[0-9a-f]{64}$$"))' images/cluster-api-provider-kubevirt.json -o json -r) && \
		printf '%s\n' "$(REGISTRY)/cluster-api-provider-kubevirt:$(IMAGE_TAG)@$$digest" > images/cluster-api-provider-kubevirt.tag
	rm -f images/cluster-api-provider-kubevirt.json
