NAME=kubernetes-worker-classes
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test generate image

test:
	helm unittest .

generate:
	cozyvalues-gen --values values.yaml --schema values.schema.json --readme README.md

image:
	docker buildx build --file images/kubernetes-worker-controller/Containerfile ../../.. \
		$(call image-tags,kubernetes-worker-controller,v$(COZYSTACK_VERSION)) \
		$(call cache-args,kubernetes-worker-controller) \
		--metadata-file images/kubernetes-worker-controller.json \
		$(BUILDX_ARGS)
	digest=$$(../../../hack/build-digest.sh images/kubernetes-worker-controller.json) && \
	echo "$(REGISTRY)/kubernetes-worker-controller:$(IMAGE_TAG)@$$digest" > images/kubernetes-worker-controller.tag
	rm --force images/kubernetes-worker-controller.json
