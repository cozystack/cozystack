NAME=flux-shard-operator
NAMESPACE=cozy-fluxcd

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test

image: image-flux-shard-operator

test:
	helm unittest .

image-flux-shard-operator:
	docker buildx build -f images/flux-shard-operator/Dockerfile ../../.. \
		$(call image-tags,flux-shard-operator,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(REGISTRY)/flux-shard-operator:latest \
		--cache-to type=inline \
		--metadata-file images/flux-shard-operator.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/flux-shard-operator:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/flux-shard-operator.json -o json -r)" \
		yq -i '.fluxShardOperator.image = strenv(IMAGE)' values.yaml
	rm -f images/flux-shard-operator.json
