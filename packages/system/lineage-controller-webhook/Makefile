NAME=lineage-controller-webhook
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test

image: image-lineage-controller-webhook

test:
	helm unittest .

image-lineage-controller-webhook:
	docker buildx build -f images/lineage-controller-webhook/Dockerfile ../../.. \
		$(call image-tags,lineage-controller-webhook,v$(COZYSTACK_VERSION)) \
		$(call cache-args,lineage-controller-webhook) \
		--metadata-file images/lineage-controller-webhook.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/lineage-controller-webhook:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/lineage-controller-webhook.json -o json -r)" \
		yq -i '.lineageControllerWebhook.image = strenv(IMAGE)' values.yaml
	rm -f images/lineage-controller-webhook.json
