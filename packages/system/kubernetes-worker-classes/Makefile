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
	echo "$(REGISTRY)/kubernetes-worker-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubernetes-worker-controller.json --output-format json --unwrapScalar)" \
		> images/kubernetes-worker-controller.tag
	rm --force images/kubernetes-worker-controller.json
