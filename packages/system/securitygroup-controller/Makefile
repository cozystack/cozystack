NAME=securitygroup-controller
NAMESPACE=cozy-securitygroup-controller

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-securitygroup-controller

image-securitygroup-controller:
	docker buildx build -f images/securitygroup-controller/Dockerfile ../../.. \
		$(call image-tags,securitygroup-controller,v$(COZYSTACK_VERSION)) \
		$(call cache-args,securitygroup-controller) \
		--metadata-file images/securitygroup-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/securitygroup-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/securitygroup-controller.json -o json -r)" \
		yq -i '.securityGroupController.image = strenv(IMAGE)' values.yaml
	rm -f images/securitygroup-controller.json
