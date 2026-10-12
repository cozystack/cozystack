NAME=migration-controller
NAMESPACE=cozy-migration-controller

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-migration-controller

image-migration-controller:
	docker buildx build -f images/migration-controller/Dockerfile ../../.. \
		$(call image-tags,migration-controller,v$(COZYSTACK_VERSION)) \
		$(call cache-args,migration-controller) \
		--metadata-file images/migration-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/migration-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/migration-controller.json -o json -r)" \
		yq -i '.migrationController.image = strenv(IMAGE)' values.yaml
	rm -f images/migration-controller.json
