NAME=cozystack-controller
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image: image-cozystack-controller

image-cozystack-controller:
	docker buildx build -f images/cozystack-controller/Dockerfile ../../.. \
		$(call image-tags,cozystack-controller,v$(COZYSTACK_VERSION)) \
		--build-arg VERSION=v$(COZYSTACK_VERSION) \
		--cache-from type=registry,ref=$(CACHE_REGISTRY)/cozystack-controller:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cozystack-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/cozystack-controller.json -o json -r)" \
		yq -i '.cozystackController.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-controller.json
