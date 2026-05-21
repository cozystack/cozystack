export NAME=kubeovn-webhook
export NAMESPACE=cozy-kubeovn

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build images/kubeovn-webhook \
		$(call image-tags,kubeovn-webhook,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(REGISTRY)/kubeovn-webhook:latest \
		--cache-to type=inline \
		--metadata-file images/kubeovn-webhook.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/kubeovn-webhook:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubeovn-webhook.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/kubeovn-webhook.json
