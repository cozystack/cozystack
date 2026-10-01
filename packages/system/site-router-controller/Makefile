NAME=site-router-controller
NAMESPACE=cozy-site-router

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-site-router-controller

image-site-router-controller:
	docker buildx build -f images/site-router-controller/Dockerfile ../../.. \
		$(call image-tags,site-router-controller,v$(COZYSTACK_VERSION)) \
		$(call cache-args,site-router-controller) \
		--metadata-file images/site-router-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/site-router-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/site-router-controller.json -o json -r)" \
		yq -i '.siteRouterController.image = strenv(IMAGE)' values.yaml
	rm -f images/site-router-controller.json
