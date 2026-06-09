export NAME=kubeovn-plunger
export NAMESPACE=cozy-kubeovn

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build -f images/kubeovn-plunger/Dockerfile ../../../ \
		--provenance false \
		$(call image-tags,kubeovn-plunger,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(CACHE_REGISTRY)/kubeovn-plunger:latest \
		--cache-to type=inline \
		--metadata-file images/kubeovn-plunger.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/kubeovn-plunger:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubeovn-plunger.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/kubeovn-plunger.json
