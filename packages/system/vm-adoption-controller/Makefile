NAME=vm-adoption-controller
NAMESPACE=cozy-forklift

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-vm-adoption-controller

image-vm-adoption-controller:
	docker buildx build -f images/controller/Dockerfile images/controller \
		$(call image-tags,vm-adoption-controller,v$(COZYSTACK_VERSION)) \
		$(call cache-args,vm-adoption-controller) \
		--metadata-file images/controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/vm-adoption-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/controller.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/controller.json
