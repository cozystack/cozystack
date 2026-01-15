NAME=lineage-controller-webhook
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image: image-lineage-controller-webhook

image-lineage-controller-webhook:
	docker buildx build -f images/lineage-controller-webhook/Dockerfile ../../.. \
		--tag $(REGISTRY)/lineage-controller-webhook:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/lineage-controller-webhook:latest \
		--cache-to type=inline \
		--metadata-file images/lineage-controller-webhook.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/lineage-controller-webhook:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/lineage-controller-webhook.json -o json -r)" \
		yq -i '.lineageControllerWebhook.image = strenv(IMAGE)' values.yaml
	rm -f images/lineage-controller-webhook.json
