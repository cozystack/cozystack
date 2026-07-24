NAME=vm-adoption-controller
NAMESPACE=cozy-forklift

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-vm-adoption-controller

image-vm-adoption-controller:
	docker buildx build -f images/controller/Dockerfile images/controller \
		--tag $(REGISTRY)/vm-adoption-controller:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/vm-adoption-controller:latest \
		--cache-to type=inline \
		--metadata-file images/controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/vm-adoption-controller:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/controller.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/controller.json
