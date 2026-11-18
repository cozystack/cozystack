export NAME=kubeovn-plunger
export NAMESPACE=cozy-kubeovn

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build -f images/kubeovn-plunger/Dockerfile ../../../ \
		$(call image-tags,kubeovn-plunger,v$(COZYSTACK_VERSION)) \
		$(call cache-args,kubeovn-plunger) \
		--metadata-file images/kubeovn-plunger.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/kubeovn-plunger:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubeovn-plunger.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/kubeovn-plunger.json
